package contextcompress

import "testing"

func TestCompressibleTool_IncludesNewIntegrationSearchTools(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"tavily_search", "perplexity_search"} {
		if !compressibleTool(name) {
			t.Fatalf("expected %s to be compressible", name)
		}
	}
}
