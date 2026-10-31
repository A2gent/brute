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
	URL      string `json:"url" jsonschema_description:"The full YouTube video URL to extract transcript from."`
	Language string `json:"language,omitempty" jsonschema_description:"Optional language code (e.g. ru, en). Defaults to the video's original spoken language."`
}

func NewYoutubeTranscriptTool() *YoutubeTranscriptTool {
	return &YoutubeTranscriptTool{}
}

func (t *YoutubeTranscriptTool) Name() string {
	return "youtube_transcript"
}

func (t *YoutubeTranscriptTool) Description() string {
	return "Extract transcript/subtitles from a YouTube video URL. Uses internal API and does not require credentials. Returns clean text in the video's original spoken language by default; pass language to pick another available track."
}

func (t *YoutubeTranscriptTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "The full YouTube video URL to extract transcript from.",
			},
			"language": map[string]interface{}{
				"type":        "string",
				"description": "Optional language code (e.g. ru, en). Defaults to the video's original spoken language.",
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

	track := pickCaptionTrack(playerCaptions, captionTracks, args.Language)
	trackURL, ok := track["baseUrl"].(string)
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

// pickCaptionTrack prefers the requested language, then the auto-generated (ASR)
// track which always matches the spoken language, then the default audio track's
// caption, and finally the first track.
func pickCaptionTrack(renderer map[string]interface{}, tracks []interface{}, language string) map[string]interface{} {
	asMap := func(v interface{}) map[string]interface{} {
		m, _ := v.(map[string]interface{})
		return m
	}
	language = strings.ToLower(strings.TrimSpace(language))
	if language != "" {
		for _, t := range tracks {
			m := asMap(t)
			code, _ := m["languageCode"].(string)
			if m != nil && strings.ToLower(code) == language {
				return m
			}
		}
	}
	for _, t := range tracks {
		if m := asMap(t); m != nil && m["kind"] == "asr" {
			return m
		}
	}
	if audio, ok := renderer["audioTracks"].([]interface{}); ok {
		idx := 0
		if f, ok := renderer["defaultAudioTrackIndex"].(float64); ok {
			idx = int(f)
		}
		if idx >= 0 && idx < len(audio) {
			if indices, ok := asMap(audio[idx])["captionTrackIndices"].([]interface{}); ok && len(indices) > 0 {
				if f, ok := indices[0].(float64); ok && int(f) >= 0 && int(f) < len(tracks) {
					if m := asMap(tracks[int(f)]); m != nil {
						return m
					}
				}
			}
		}
	}
	return asMap(tracks[0])
}
