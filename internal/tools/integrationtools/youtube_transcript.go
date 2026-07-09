package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/A2gent/brute/internal/tools"
)

var ytRegex = regexp.MustCompile(`(?:youtube\.com\/(?:[^\/]+\/.+\/|(?:v|e(?:mbed)?)\/|.*[?&]v=)|youtu\.be\/)([^"&?\/\s]{11})`)

type YoutubeTranscriptTool struct{}

type YoutubeTranscriptArgs struct {
	URL string `json:"url" jsonschema_description:"The full YouTube video URL to extract transcript from."`
}

func NewYoutubeTranscriptTool() *YoutubeTranscriptTool {
	return &YoutubeTranscriptTool{}
}

func (t *YoutubeTranscriptTool) Name() string {
	return "youtube_transcript"
}

func (t *YoutubeTranscriptTool) Description() string {
	return "Extract transcript/subtitles from a YouTube video URL. Uses internal API and does not require credentials. Returns clean text."
}

func (t *YoutubeTranscriptTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "The full YouTube video URL to extract transcript from.",
			},
		},
		"required": []string{"url"},
	}
}

func (t *YoutubeTranscriptTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var args YoutubeTranscriptArgs
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	matches := ytRegex.FindStringSubmatch(args.URL)
	if len(matches) < 2 {
		return &tools.Result{Success: false, Error: "invalid youtube url"}, nil
	}
	videoID := matches[1]

	reqBody, _ := json.Marshal(map[string]interface{}{
		"context": map[string]interface{}{
			"client": map[string]interface{}{
				"clientName":    "ANDROID",
				"clientVersion": "20.10.38",
			},
		},
		"videoId": videoID,
	})

	req, _ := http.NewRequestWithContext(ctx, "POST", "https://www.youtube.com/youtubei/v1/player?prettyPrint=false", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "com.google.android.youtube/20.10.38 (Linux; U; Android 14)")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call InnerTube API: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return &tools.Result{Success: false, Error: "failed to parse youtube response"}, nil
	}

	captions, ok := data["captions"].(map[string]interface{})
	if !ok {
		return &tools.Result{Success: false, Error: "no captions available for this video"}, nil
	}
	playerCaptions, ok := captions["playerCaptionsTracklistRenderer"].(map[string]interface{})
	if !ok {
		return &tools.Result{Success: false, Error: "no caption tracklist found"}, nil
	}
	captionTracks, ok := playerCaptions["captionTracks"].([]interface{})
	if !ok || len(captionTracks) == 0 {
		return &tools.Result{Success: false, Error: "no caption tracks available"}, nil
	}

	firstTrack := captionTracks[0].(map[string]interface{})
	trackURL, ok := firstTrack["baseUrl"].(string)
	if !ok {
		return &tools.Result{Success: false, Error: "invalid track url"}, nil
	}

	req2, _ := http.NewRequestWithContext(ctx, "GET", trackURL, nil)
	resp2, err := client.Do(req2)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch transcript: %w", err)
	}
	defer resp2.Body.Close()

	body2, _ := io.ReadAll(resp2.Body)

	xmlRegex := regexp.MustCompile(`<[^>]*>`)
	cleanText := xmlRegex.ReplaceAllString(string(body2), " ")
	cleanText = strings.Join(strings.Fields(cleanText), " ")

	return &tools.Result{
		Success: true,
		Output:  cleanText,
	}, nil
}
