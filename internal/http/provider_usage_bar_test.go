package http

import (
	"encoding/json"
	"testing"
)

func TestProviderUsageBarLongTermJSON(t *testing.T) {
	for _, longTerm := range []bool{false, true} {
		payload, err := json.Marshal(ProviderUsageBar{LongTerm: longTerm})
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]interface{}
		if err := json.Unmarshal(payload, &fields); err != nil {
			t.Fatal(err)
		}
		value, present := fields["long_term"]
		if present != longTerm || (longTerm && value != true) {
			t.Fatalf("long_term=%t encoded incorrectly: %s", longTerm, payload)
		}
	}
}
