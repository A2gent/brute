package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

// Opt-in, read-only replay through the production request builder. Legacy logs
// contain image_file.bytes but often no surviving PNG. In size-only mode, model
// the exact base64 length in today's admission_images channel; never call Jev.
func TestScreenshotRetentionArchiveReplay(t *testing.T) {
	path := os.Getenv("BRUTE_SCREENSHOT_REPLAY")
	if path == "" {
		t.Skip("set BRUTE_SCREENSHOT_REPLAY to an archived JSONL session")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	sizeOnly := os.Getenv("BRUTE_SCREENSHOT_REPLAY_LEGACY_BYTES") == "true"
	sess := session.New("screenshot-replay")
	ag := screenshotAgent(t, Config{})
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 128*1024*1024)
	seen := map[string]bool{}
	requests, screenshots, reconstructed := 0, 0, 0
	var before, after, lastBefore, lastAfter, peakBefore, peakAfter int64
	for scanner.Scan() {
		var record session.JSONLRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record.Message == nil || seen[record.Message.ID] {
			continue
		}
		m := *record.Message
		seen[m.ID] = true
		if m.Role == "assistant" {
			// Each recorded assistant turn represents the request preceding it.
			lastBefore = 0
			for _, msg := range ag.getActiveConversationMessages(sess) {
				for _, img := range msg.Images {
					lastBefore += int64(len(img.DataBase64))
				}
				for _, tr := range msg.ToolResults {
					for _, img := range toolScreenshotImages(tr.Metadata) {
						lastBefore += int64(len(img.DataBase64))
					}
				}
			}
			lastAfter = 0
			for _, data := range requestImageData(ag.buildRequest(context.Background(), sess)) {
				lastAfter += int64(len(data))
			}
			before, after = before+lastBefore, after+lastAfter
			peakBefore, peakAfter = max(peakBefore, lastBefore), max(peakAfter, lastAfter)
			requests++
		}
		for i := range m.ToolResults {
			tr := &m.ToolResults[i]
			if sizeOnly && len(toolScreenshotImages(tr.Metadata)) == 0 {
				legacy, _ := tr.Metadata["image_file"].(map[string]interface{})
				size, _ := legacy["bytes"].(float64)
				action, _ := legacy["action"].(string)
				if action == "screenshot" && size > 0 {
					// The bytes are placeholders, not fabricated screenshot contents.
					// Length is exact: base64 emits four bytes per three source bytes.
					tr.Metadata["admission_images"] = []interface{}{map[string]interface{}{"media_type": "image/png", "data_base64": strings.Repeat("A", 4*((int(size)+2)/3))}}
					reconstructed++
				}
			}
			screenshots += len(toolScreenshotImages(tr.Metadata))
		}
		sess.Messages = append(sess.Messages, m)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if requests == 0 || screenshots <= DefaultScreenshotKeepLatest || before == 0 {
		t.Fatalf("not enough screenshot data: requests=%d screenshots=%d bytes=%d", requests, screenshots, before)
	}
	if after >= before {
		t.Fatalf("retention did not reduce payload: before=%d after=%d", before, after)
	}
	t.Logf("requests=%d screenshots=%d size_only_reconstructed=%d", requests, screenshots, reconstructed)
	t.Logf("base64 bytes/request: mean %.1f -> %.1f; peak %d -> %d; last %d -> %d", float64(before)/float64(requests), float64(after)/float64(requests), peakBefore, peakAfter, lastBefore, lastAfter)
	t.Logf("total base64 bytes: %d -> %d (%.2f%% reduction)", before, after, 100*(1-float64(after)/float64(before)))
}
