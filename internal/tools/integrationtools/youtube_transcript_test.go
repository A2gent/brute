package integrationtools

import "testing"

func TestPickCaptionTrack(t *testing.T) {
	en := map[string]interface{}{"languageCode": "en", "baseUrl": "en"}
	ru := map[string]interface{}{"languageCode": "ru", "baseUrl": "ru"}
	asr := map[string]interface{}{"languageCode": "pl", "kind": "asr", "baseUrl": "asr"}

	t.Run("prefers asr over first", func(t *testing.T) {
		got := pickCaptionTrack(map[string]interface{}{}, []interface{}{en, asr}, "")
		if got["baseUrl"] != "asr" {
			t.Fatalf("got %v", got["baseUrl"])
		}
	})
	t.Run("explicit language wins", func(t *testing.T) {
		got := pickCaptionTrack(map[string]interface{}{}, []interface{}{en, ru, asr}, "RU")
		if got["baseUrl"] != "ru" {
			t.Fatalf("got %v", got["baseUrl"])
		}
	})
	t.Run("default audio track", func(t *testing.T) {
		r := map[string]interface{}{"audioTracks": []interface{}{map[string]interface{}{"captionTrackIndices": []interface{}{float64(1)}}}}
		got := pickCaptionTrack(r, []interface{}{en, ru}, "")
		if got["baseUrl"] != "ru" {
			t.Fatalf("got %v", got["baseUrl"])
		}
	})
	t.Run("falls back to first", func(t *testing.T) {
		got := pickCaptionTrack(map[string]interface{}{}, []interface{}{en, ru}, "")
		if got["baseUrl"] != "en" {
			t.Fatalf("got %v", got["baseUrl"])
		}
	})
}
