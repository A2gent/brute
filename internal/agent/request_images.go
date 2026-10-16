package agent

import (
	"encoding/base64"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

// fitRequestImages is the single provider-bound guard for user attachments,
// tool screenshots and images stored before ingestion-time fitting existed.
// An oversized image is rejected by the provider on every turn, and image aging
// runs only after completed answers, so the session could never recover.
func fitRequestImages(messages []llm.Message) {
	for i := range messages {
		kept := messages[i].Images[:0]
		for _, img := range messages[i].Images {
			fitted, err := llm.FitImage(img)
			// Undecodable but small data keeps the previous pass-through behavior.
			if err != nil && len(img.DataBase64) <= base64.StdEncoding.EncodedLen(llm.ProviderImageMaxBytes) {
				fitted, err = img, nil
			}
			if err != nil {
				logging.Warn("Dropping image the provider cannot accept: %v", err)
				messages[i].Content += "\n[image omitted: cannot decode for the model: " + err.Error() + "]"
				continue
			}
			kept = append(kept, fitted)
		}
		messages[i].Images = kept
	}
}
