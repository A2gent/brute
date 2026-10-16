package agent

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/A2gent/brute/internal/contextcompress"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/session"
)

const (
	DefaultScreenshotKeepLatest = 2
	screenshotRetentionMetadata = "screenshot_retention"
)

// ScreenshotGate is non-nil when Jev screenshot retention is opted in. A missing
// client is deliberately different from opting out: unavailable Jev keeps images.
type ScreenshotGate struct {
	Client        *jev.Client
	Threshold     float64
	MinConfidence float64
	Secrets       []string
}

func screenshotKey(toolCallID string, index int) string {
	return fmt.Sprintf("%s/%d", toolCallID, index)
}

func screenshotDecision(message session.Message, toolCallID string, index int) string {
	decisions, _ := message.Metadata[screenshotRetentionMetadata].(map[string]interface{})
	decision, _ := decisions[screenshotKey(toolCallID, index)].(string)
	return decision
}

// User attachments share the image-message window but never go to the optional
// classifier. Original payloads stay in session history for replay and recovery.
func (a *Agent) prepareScreenshotRetention(sess *session.Session, messages []session.Message) {
	type candidate struct {
		message int
		key     string
		step    int
		image   llm.Image
		user    bool
	}
	originals := make(map[string]*session.Message, len(sess.Messages))
	steps := make(map[string]int, len(sess.Messages))
	step := 0
	for i := range sess.Messages {
		msg := &sess.Messages[i]
		if msg.Role == "assistant" {
			step++
		}
		originals[msg.ID], steps[msg.ID] = msg, step
	}
	ages := imageAnswerAges(sess.Messages)
	var screenshots []candidate
	var imageMessages []int
	for mi, msg := range messages {
		before := len(screenshots)
		if msg.Role == "user" {
			for i, img := range sessionImagesToLLM(msg.Images) {
				screenshots = append(screenshots, candidate{mi, screenshotKey("attachment", i), steps[msg.ID], img, true})
			}
		}
		for _, tr := range msg.ToolResults {
			for i, img := range toolScreenshotImages(tr.Metadata) {
				screenshots = append(screenshots, candidate{mi, screenshotKey(tr.ToolCallID, i), steps[msg.ID], img, false})
			}
		}
		if len(screenshots) > before {
			imageMessages = append(imageMessages, mi)
		}
	}
	keepLatest := a.config.ScreenshotKeepLatest
	if keepLatest <= 0 {
		keepLatest = DefaultScreenshotKeepLatest
	}
	firstKeptMessage := 0
	if len(imageMessages) > keepLatest {
		firstKeptMessage = imageMessages[len(imageMessages)-keepLatest]
	}
	var older []candidate
	for _, image := range screenshots {
		if image.message >= firstKeptMessage && ages[messages[image.message].ID] < imageAnswerLimit {
			continue
		}
		msg := &messages[image.message]
		if original := originals[msg.ID]; original != nil {
			if original.Metadata == nil {
				original.Metadata = make(map[string]interface{})
			}
			msg.Metadata = original.Metadata
		}
		decisions, _ := msg.Metadata[screenshotRetentionMetadata].(map[string]interface{})
		if _, decided := decisions[image.key]; !decided {
			older = append(older, image)
		}
	}
	if len(older) == 0 {
		return
	}
	keep := make([]bool, len(older))
	if gate := a.config.ScreenshotGate; gate != nil {
		// Fail open for missing credentials, unsafe task text, invalid images,
		// timeouts and partial batches. Persist that outcome too, without retries.
		var indexes []int
		var images []llm.Image
		for i, image := range older {
			if !image.user {
				keep[i] = true
				indexes = append(indexes, i)
				images = append(images, image.image)
			}
		}
		task := contextcompress.RelevanceTask(sess)
		if len(images) > 0 && task != "" && jev.SafeEgressText(task, gate.Secrets) {
			threshold, confidence := gate.Threshold, gate.MinConfidence
			if threshold <= 0 || threshold > 1 || math.IsNaN(threshold) {
				threshold = 0.5
			}
			if confidence <= 0 || confidence > 1 || math.IsNaN(confidence) {
				confidence = 0.8
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			scores, err := jev.ScoreScreenshotRelevance(ctx, gate.Client, task, images, confidence)
			cancel()
			if err == nil {
				for i, score := range scores {
					keep[indexes[i]] = score >= threshold
				}
			}
		}
	}
	for i, image := range older {
		msg := &messages[image.message]
		if msg.Metadata == nil {
			msg.Metadata = make(map[string]interface{})
		}
		decisions, _ := msg.Metadata[screenshotRetentionMetadata].(map[string]interface{})
		if decisions == nil {
			decisions = make(map[string]interface{})
			msg.Metadata[screenshotRetentionMetadata] = decisions
		}
		decision := "keep"
		if !keep[i] {
			fallback := fmt.Sprintf("[screenshot from step %d omitted; re-capture if needed]", image.step)
			decision = a.imageOmission(sess, image.image, fallback)
		}
		decisions[image.key] = decision
		sess.MarkMessageChanged(msg.ID)
	}
}

func toolScreenshotImages(metadata map[string]interface{}) []llm.Image {
	raw, _ := metadata["admission_images"].([]interface{})
	if len(raw) == 0 && metadata["image_inline"] != nil {
		raw = []interface{}{metadata["image_inline"]}
	}
	images := make([]llm.Image, 0, len(raw))
	for _, value := range raw {
		image, _ := value.(map[string]interface{})
		mediaType, _ := image["media_type"].(string)
		data, _ := image["data_base64"].(string)
		url, _ := image["url"].(string)
		// Path-backed image_inline is resolved (and size-limited) by the provider.
		// Leave that channel intact instead of replacing it with an empty image.
		if data == "" && url == "" {
			continue
		}
		images = append(images, llm.Image{MediaType: mediaType, DataBase64: data, URL: url})
	}
	return images
}
