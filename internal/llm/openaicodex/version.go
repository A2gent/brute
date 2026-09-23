package openaicodex

// ClientVersion is the codex_cli_rs version sent in User-Agent and client_version
// query param for OAuth /models discovery. Keep in sync with official models.json.
const ClientVersion = "0.155.0"

// UserAgent returns the Codex CLI User-Agent header value.
func UserAgent() string {
	return "codex_cli_rs/" + ClientVersion
}
