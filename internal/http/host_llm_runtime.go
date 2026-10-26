package http

import (
	"net"
	"net/url"

	"github.com/A2gent/brute/internal/config"
)

// Host children have the CLI and the session's MCP server on the same machine.
// Sending Claude through the OpenAI proxy loses that session-scoped bridge.
func (s *Server) useLocalHostLLM(ref string) bool {
	return s.config.HostClaudeCLI &&
		(config.IsClaudeProviderRef(ref) || config.IsFallbackAggregateRef(ref) || ref == string(config.ProviderFallback))
}

// Docker uses host.docker.internal; only host runtimes receive a loopback URL.
func (s *Server) parentProxyEnvArgs(provider string, proxyBaseURL string) []string {
	args := s.autoRouterDockerEnvArgs(provider)
	args = append(args,
		"--env", "AAGENT_PROVIDER="+provider,
		"--env", "A2GENT_PARENT_PROXY_URL="+proxyBaseURL,
		"--env", "LM_STUDIO_BASE_URL="+proxyBaseURL+"/providers/lmstudio",
		"--env", "OPENAI_API_KEY=a2gent-proxy",
		"--env", "OPENAI_BASE_URL="+proxyBaseURL+"/providers/openai",
		"--env", "KIMI_API_KEY=a2gent-proxy",
		"--env", "KIMI_BASE_URL="+proxyBaseURL+"/providers/kimi",
		"--env", "GOOGLE_API_KEY=a2gent-proxy",
		"--env", "GOOGLE_BASE_URL="+proxyBaseURL+"/providers/google",
		"--env", "OPENROUTER_API_KEY=a2gent-proxy",
		"--env", "OPENROUTER_BASE_URL="+proxyBaseURL+"/providers/openrouter",
	)
	endpoint, err := url.Parse(proxyBaseURL)
	if err == nil {
		ip := net.ParseIP(endpoint.Hostname())
		if ip != nil && ip.IsLoopback() {
			encoded, _ := config.EncodeHostLLMEnv(s.config)
			// Preserve the host's CLI login and configured profiles, not fake proxy auth.
			return append(args, "--env", config.HostLLMEnvKey+"="+encoded)
		}
	}
	return append(args, "--env", "ANTHROPIC_API_KEY=a2gent-proxy")
}
