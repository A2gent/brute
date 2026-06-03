package tui

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"nhooyr.io/websocket"
)

const (
	defaultA2ATransport = "grpc"
	defaultA2AGRPCAddr  = "a2gent.net:9001"
	a2aTunnelMethodPath = "/square.tunnel.v1.TunnelService/Connect"
)

func (m Model) handleA2ACommand(args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		m.messages = append(m.messages, message{
			role:      "system",
			content:   "Usage:\n  /a2a status\n  /a2a connect <api_key> [grpc|websocket] [endpoint]\n  /a2a disconnect",
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "status":
		return m.a2aStatus()
	case "connect":
		return m.a2aConnect(args[1:])
	case "disconnect":
		return m.a2aDisconnect()
	default:
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Unknown /a2a subcommand: %s", sub),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}
}

func findA2AIntegration(integrations []*storage.Integration) *storage.Integration {
	for _, integration := range integrations {
		if integration != nil && integration.Provider == "a2_registry" {
			return integration
		}
	}
	return nil
}

func normalizeA2ATransport(raw string) (string, error) {
	transport := strings.ToLower(strings.TrimSpace(raw))
	if transport == "" {
		transport = defaultA2ATransport
	}
	switch transport {
	case "grpc", "websocket":
		return transport, nil
	default:
		return "", fmt.Errorf("unsupported transport: %s (use grpc or websocket)", raw)
	}
}

func resolveA2AEndpoint(transport, endpoint string) (string, error) {
	ep := strings.TrimSpace(endpoint)
	if transport == "grpc" {
		if ep == "" {
			return "", fmt.Errorf("missing gRPC endpoint")
		}
		parts := strings.Split(ep, ":")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return "", fmt.Errorf("gRPC endpoint must be host:port")
		}
		return ep, nil
	}
	if ep == "" {
		return "", fmt.Errorf("missing WebSocket endpoint")
	}
	u, err := url.Parse(ep)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid WebSocket endpoint")
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return "", fmt.Errorf("WebSocket endpoint must start with ws:// or wss://")
	}
	return ep, nil
}

func probeA2AConnectivity(ctx context.Context, transport, endpoint, apiKey string) error {
	if transport == "websocket" {
		dialCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(dialCtx, endpoint, &websocket.DialOptions{
			HTTPHeader: map[string][]string{
				"Authorization": {"Bearer " + apiKey},
			},
		})
		if err != nil {
			return err
		}
		_ = conn.Close(websocket.StatusNormalClosure, "ok")
		return nil
	}

	dialCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	conn, err := grpc.DialContext( //nolint:staticcheck
		dialCtx,
		endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return err
	}
	defer conn.Close()

	streamCtx, streamCancel := context.WithTimeout(ctx, 12*time.Second)
	defer streamCancel()
	streamCtx = metadata.NewOutgoingContext(streamCtx, metadata.Pairs("x-api-key", apiKey))
	stream, err := conn.NewStream(
		streamCtx,
		&grpc.StreamDesc{
			StreamName:    "Connect",
			ServerStreams: true,
			ClientStreams: true,
		},
		a2aTunnelMethodPath,
	)
	if err != nil {
		return err
	}
	_ = stream.CloseSend()
	return nil
}

func maskedKeyForStatus(apiKey string) string {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return "(empty)"
	}
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}

func (m Model) a2aStatus() (tea.Model, tea.Cmd) {
	integrations, err := m.sessionManager.ListIntegrations()
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Failed to read A2A integration: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	integration := findA2AIntegration(integrations)
	if integration == nil {
		m.messages = append(m.messages, message{
			role:      "system",
			content:   "A2A status: disconnected (no integration configured)",
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	transport, err := normalizeA2ATransport(integration.Config["transport"])
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("A2A status: invalid transport in config: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	endpoint := strings.TrimSpace(integration.Config["square_grpc_addr"])
	if transport == "websocket" {
		endpoint = strings.TrimSpace(integration.Config["square_ws_url"])
	}
	resolvedEndpoint, err := resolveA2AEndpoint(transport, endpoint)
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("A2A status: invalid endpoint in config: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	apiKey := strings.TrimSpace(integration.Config["api_key"])
	if apiKey == "" {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   "A2A status: integration exists but api_key is empty",
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	connectedText := "no"
	if integration.Enabled {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := probeA2AConnectivity(ctx, transport, resolvedEndpoint, apiKey); err == nil {
			connectedText = "yes"
		}
	}

	content := fmt.Sprintf(
		"A2A status:\n  enabled:   %t\n  transport: %s\n  endpoint:  %s\n  api_key:   %s\n  connected: %s",
		integration.Enabled,
		transport,
		resolvedEndpoint,
		maskedKeyForStatus(apiKey),
		connectedText,
	)
	m.messages = append(m.messages, message{
		role:      "system",
		content:   content,
		timestamp: time.Now(),
	})
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
	return m, nil
}

func (m Model) a2aConnect(args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   "Usage: /a2a connect <api_key> [grpc|websocket] [endpoint]",
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	integrations, err := m.sessionManager.ListIntegrations()
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Failed to read integrations: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	existing := findA2AIntegration(integrations)
	apiKey := strings.TrimSpace(args[0])
	transportArg := ""
	endpointArg := ""
	if len(args) >= 2 {
		transportArg = args[1]
	}
	if len(args) >= 3 {
		endpointArg = args[2]
	}

	transport := defaultA2ATransport
	if existing != nil {
		if t := strings.TrimSpace(existing.Config["transport"]); t != "" {
			transport = t
		}
	}
	if transportArg != "" {
		transport = transportArg
	}
	transport, err = normalizeA2ATransport(transport)
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   err.Error(),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	endpoint := ""
	if existing != nil {
		if transport == "grpc" {
			endpoint = strings.TrimSpace(existing.Config["square_grpc_addr"])
		} else {
			endpoint = strings.TrimSpace(existing.Config["square_ws_url"])
		}
	}
	if endpoint == "" && transport == "grpc" {
		endpoint = defaultA2AGRPCAddr
	}
	if endpointArg != "" {
		endpoint = endpointArg
	}

	endpoint, err = resolveA2AEndpoint(transport, endpoint)
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Invalid endpoint: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	now := time.Now()
	integration := existing
	if integration == nil {
		integration = &storage.Integration{
			ID:        uuid.New().String(),
			Provider:  "a2_registry",
			Name:      "A2 Registry",
			Mode:      "duplex",
			Enabled:   true,
			Config:    map[string]string{},
			CreatedAt: now,
		}
	}
	if integration.Config == nil {
		integration.Config = map[string]string{}
	}
	integration.Provider = "a2_registry"
	integration.Mode = "duplex"
	integration.Enabled = true
	integration.Name = "A2 Registry"
	integration.Config["api_key"] = apiKey
	integration.Config["transport"] = transport
	if transport == "grpc" {
		integration.Config["square_grpc_addr"] = endpoint
	} else {
		integration.Config["square_ws_url"] = endpoint
	}
	integration.UpdatedAt = now

	if err := m.sessionManager.SaveIntegration(integration); err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Failed to save A2A integration: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	connectedText := "no"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := probeA2AConnectivity(ctx, transport, endpoint, apiKey); err == nil {
		connectedText = "yes"
	}

	m.messages = append(m.messages, message{
		role: "system",
		content: fmt.Sprintf(
			"A2A connected/configured.\n  transport: %s\n  endpoint:  %s\n  api_key:   %s\n  connected: %s",
			transport,
			endpoint,
			maskedKeyForStatus(apiKey),
			connectedText,
		),
		timestamp: time.Now(),
	})
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
	return m, nil
}

func (m Model) a2aDisconnect() (tea.Model, tea.Cmd) {
	integrations, err := m.sessionManager.ListIntegrations()
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Failed to read integrations: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	existing := findA2AIntegration(integrations)
	if existing == nil {
		m.messages = append(m.messages, message{
			role:      "system",
			content:   "A2A is already disconnected.",
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	if err := m.sessionManager.DeleteIntegration(existing.ID); err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Failed to disconnect A2A: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}

	m.messages = append(m.messages, message{
		role:      "system",
		content:   "Disconnected from A2A network and removed stored credentials.",
		timestamp: time.Now(),
	})
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
	return m, nil
}

// createNewSession creates a new session
