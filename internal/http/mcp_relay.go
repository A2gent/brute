package http

// Docker MCP is a reverse channel: the child never exposes its loopback MCP
// listener, and neither a callback URL nor a parent session is trusted.
import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/lmstudio"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

const (
	parentMCPKeyEnv       = "A2GENT_PARENT_MCP_KEY"
	mcpRelayKeyLabel      = "a2gent.mcp_relay_key"
	mcpRelayHeader        = "X-A2gent-MCP-Relay"
	mcpRelaySessionHeader = "X-A2gent-Session-ID"
)

// Bound unauthenticated Docker checks as well as authenticated CLI capacity.
var mcpRelayAuthSlots = make(chan struct{}, 4)

type mcpRelayContextKey struct{}
type mcpRelayChildKey struct{}

type mcpRelayFrame struct {
	ID       string                `json:"id"`
	Message  *mcpBridgeRPCMessage  `json:"message,omitempty"`
	Response *mcpBridgeRPCResponse `json:"response,omitempty"`
	Cancel   bool                  `json:"cancel,omitempty"`
}

type mcpRelay struct {
	id, sessionID, runtime, credential string
	ctx                                context.Context
	cancel                             context.CancelFunc
	conn                               *websocket.Conn
	writeMu                            sync.Mutex
	mu                                 sync.Mutex
	claimed                            bool
	pending                            map[string]chan *mcpBridgeRPCResponse
}

func randomMCPRelaySecret() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func mcpRelayKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Docker's current label is the credential registry. It survives a parent
// restart, but a stopped/replaced container cannot authenticate with an old key.
func validMCPRelayRuntime(ctx context.Context, key string) (string, bool) {
	separator := strings.LastIndexByte(key, '.')
	if separator < 1 || len(key[separator+1:]) != 64 {
		return "", false
	}
	runtime := key[:separator]
	if !dockerContainerIDPattern.MatchString(runtime) {
		return "", false
	}
	select {
	case mcpRelayAuthSlots <- struct{}{}:
		defer func() { <-mcpRelayAuthSlots }()
	default:
		return "", false
	}
	if ctx.Err() != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := runCommand(ctx, "docker", "inspect", "--format", `{{.State.Running}} {{index .Config.Labels "a2gent.local_agent"}} {{index .Config.Labels "a2gent.mcp_relay_key"}}`, runtime)
	want := "true true " + mcpRelayKeyHash(key)
	return runtime, err == nil && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(out)), []byte(want)) == 1
}

func (relay *mcpRelay) write(frame mcpRelayFrame) error {
	relay.writeMu.Lock()
	defer relay.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return wsjson.Write(ctx, relay.conn, frame)
}

func (relay *mcpRelay) close() {
	relay.cancel()
	_ = relay.conn.CloseNow()
}

func (s *Server) handleMCPRelay(w http.ResponseWriter, r *http.Request) {
	if !s.llmProxyEnabled() {
		s.errorResponse(w, http.StatusNotFound, "LLM provider proxy is disabled")
		return
	}
	key := mcpBridgeBearerToken(r)
	runtime, ok := validMCPRelayRuntime(r.Context(), key)
	if !ok {
		s.errorResponse(w, http.StatusUnauthorized, "invalid Docker MCP relay credential")
		return
	}
	sessionID := r.Header.Get(mcpRelaySessionHeader)
	if sessionID == "" || len(sessionID) > 128 {
		s.errorResponse(w, http.StatusBadRequest, "child session is required")
		return
	}
	// Browser origins are never part of this server-to-server protocol.
	if r.Header.Get("Origin") != "" {
		s.errorResponse(w, http.StatusForbidden, "browser relay connections are forbidden")
		return
	}
	id, err := randomMCPRelaySecret()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "cannot create relay")
		return
	}
	// Match the child's existing ten-minute completion deadline. In-flight tools
	// cannot outlive the invocation even when a peer disappears silently.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	relay := &mcpRelay{id: id, sessionID: sessionID, runtime: runtime, credential: mcpRelayKeyHash(key), ctx: ctx, cancel: cancel, claimed: true, pending: make(map[string]chan *mcpBridgeRPCResponse)}
	s.mcpBridge.mu.Lock()
	runtimeCount := 0
	for _, active := range s.mcpBridge.relays {
		if active.runtime == runtime {
			runtimeCount++
		}
	}
	if len(s.mcpBridge.relays) >= 32 || runtimeCount >= 4 {
		s.mcpBridge.mu.Unlock()
		s.errorResponse(w, http.StatusTooManyRequests, "MCP relay capacity exceeded")
		return
	}
	// Reserve before upgrading, so concurrent handshakes cannot exceed quotas.
	s.mcpBridge.relays[id] = relay
	s.mcpBridge.mu.Unlock()
	defer func() { s.mcpBridge.mu.Lock(); delete(s.mcpBridge.relays, id); s.mcpBridge.mu.Unlock() }()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	relay.mu.Lock()
	relay.conn, relay.claimed = conn, false
	relay.mu.Unlock()
	defer relay.close()
	stop := context.AfterFunc(ctx, func() { _ = conn.CloseNow() })
	defer stop()
	conn.SetReadLimit(mcpBridgeMaxBodyBytes)
	if err := relay.write(mcpRelayFrame{ID: id}); err != nil {
		return
	}
	for {
		var frame mcpRelayFrame
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			return
		}
		if frame.Response == nil {
			return
		}
		relay.mu.Lock()
		ch := relay.pending[frame.ID]
		relay.mu.Unlock()
		if ch != nil {
			select {
			case ch <- frame.Response:
			default:
			}
		}
	}
}

func (s *Server) claimMCPRelay(r *http.Request) (*mcpRelay, error) {
	id := r.Header.Get(mcpRelayHeader)
	if id == "" {
		if r.Header.Get(mcpRelaySessionHeader) != "" {
			return nil, errors.New("incomplete MCP relay credentials")
		}
		return nil, nil
	}
	s.mcpBridge.mu.Lock()
	relay := s.mcpBridge.relays[id]
	s.mcpBridge.mu.Unlock()
	if relay == nil {
		return nil, errors.New("invalid or revoked MCP relay")
	}
	if _, ok := validMCPRelayRuntime(r.Context(), mcpBridgeBearerToken(r)); !ok {
		return nil, errors.New("Docker MCP relay runtime has stopped or rotated credentials")
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.ctx.Err() != nil || relay.claimed || relay.sessionID != r.Header.Get(mcpRelaySessionHeader) || subtle.ConstantTimeCompare([]byte(relay.credential), []byte(mcpRelayKeyHash(mcpBridgeBearerToken(r)))) != 1 {
		return nil, errors.New("MCP relay credential does not match invocation")
	}
	relay.claimed = true
	return relay, nil
}

func (relay *mcpRelay) call(ctx context.Context, msg *mcpBridgeRPCMessage) *mcpBridgeRPCResponse {
	failure := func() *mcpBridgeRPCResponse {
		return &mcpBridgeRPCResponse{JSONRPC: "2.0", ID: msg.ID, Error: &mcpBridgeRPCError{Code: -32603, Message: "child MCP relay closed or canceled"}}
	}
	if ctx.Err() != nil || relay.ctx.Err() != nil {
		return failure()
	}
	id, err := randomMCPRelaySecret()
	if err != nil {
		return failure()
	}
	ch := make(chan *mcpBridgeRPCResponse, 1)
	relay.mu.Lock()
	if len(relay.pending) >= 32 {
		relay.mu.Unlock()
		return failure()
	}
	relay.pending[id] = ch
	relay.mu.Unlock()
	defer func() { relay.mu.Lock(); delete(relay.pending, id); relay.mu.Unlock() }()
	if err := relay.write(mcpRelayFrame{ID: id, Message: msg}); err != nil {
		relay.close()
		return failure()
	}
	select {
	case resp := <-ch:
		resp.ID = msg.ID
		return resp
	case <-ctx.Done():
		_ = relay.write(mcpRelayFrame{ID: id, Cancel: true})
		return failure()
	case <-relay.ctx.Done():
		return failure()
	}
}

// Consume the bootstrap secret before any tool/CLI subprocess inherits the
// environment. It remains only in the child server, never in prompts or config.
func consumeParentMCPKey() string {
	key := os.Getenv(parentMCPKeyEnv)
	_ = os.Unsetenv(parentMCPKeyEnv)
	return key
}

func (s *Server) prepareParentMCPRelay(parentURL string) func(context.Context, *llm.ChatRequest) (http.Header, func(), error) {
	return func(ctx context.Context, req *llm.ChatRequest) (http.Header, func(), error) {
		if req.SessionID == "" {
			return nil, nil, nil
		}
		sess, err := s.sessionManager.Get(req.SessionID)
		if err != nil {
			return nil, nil, fmt.Errorf("child MCP session unavailable: %w", err)
		}
		u, err := url.Parse(strings.TrimRight(parentURL, "/") + "/mcp-relay")
		if err != nil {
			return nil, nil, err
		}
		switch u.Scheme {
		case "http":
			u.Scheme = "ws"
		case "https":
			u.Scheme = "wss"
		default:
			return nil, nil, errors.New("invalid parent relay URL")
		}
		headers := make(http.Header)
		headers.Set("Authorization", "Bearer "+s.parentMCPKey)
		headers.Set(mcpRelaySessionHeader, req.SessionID)
		conn, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: headers})
		if err != nil && resp != nil && resp.Body != nil {
			defer resp.Body.Close()
		}
		if err != nil {
			return nil, nil, errors.New("cannot connect to authenticated parent MCP relay")
		}
		conn.SetReadLimit(mcpBridgeMaxBodyBytes)
		helloCtx, helloCancel := context.WithTimeout(ctx, 10*time.Second)
		defer helloCancel()
		var hello mcpRelayFrame
		if err := wsjson.Read(helloCtx, conn, &hello); err != nil || hello.ID == "" {
			_ = conn.CloseNow()
			return nil, nil, errors.New("invalid parent MCP relay handshake")
		}

		childCtx, cancel := context.WithCancel(context.WithValue(ctx, mcpRelayChildKey{}, true))
		relay := &mcpRelay{conn: conn, ctx: childCtx, cancel: cancel}
		stop := context.AfterFunc(childCtx, func() { _ = conn.CloseNow() })
		cleanup := func() { relay.close(); stop() }
		go func() {
			defer cleanup()
			var mu sync.Mutex
			calls := make(map[string]context.CancelFunc)
			for {
				var frame mcpRelayFrame
				if err := wsjson.Read(childCtx, conn, &frame); err != nil {
					return
				}
				mu.Lock()
				if frame.Cancel {
					if cancel := calls[frame.ID]; cancel != nil {
						cancel()
					}
					mu.Unlock()
					continue
				}
				if frame.Message == nil || frame.Message.isNotification() || len(calls) >= 32 || calls[frame.ID] != nil {
					mu.Unlock()
					return
				}
				callCtx, cancelCall := context.WithCancel(childCtx)
				calls[frame.ID] = cancelCall
				mu.Unlock()
				go func(frame mcpRelayFrame) {
					defer cancelCall()
					defer func() { mu.Lock(); delete(calls, frame.ID); mu.Unlock() }()
					response := s.dispatchMCPBridgeMessage(callCtx, sess, frame.Message)
					if err := relay.write(mcpRelayFrame{ID: frame.ID, Response: response}); err != nil {
						cleanup()
					}
				}(frame)
			}
		}()
		headers.Set(mcpRelayHeader, hello.ID)
		return headers, cleanup, nil
	}
}

func (s *Server) configureParentMCPRelay(client *lmstudio.Client, parentURL string) {
	if s.parentMCPKey != "" {
		client.PrepareRequest = s.prepareParentMCPRelay(parentURL)
	}
}

// CLI-native equivalents are relayed only for Docker invocations. The normal
// loopback bridge continues to exclude them in favor of local CLI tools.
func mcpBridgeToolExposedContext(ctx context.Context, name string) bool {
	if mcpBridgeToolExposed(name) {
		return true
	}
	if remote, _ := ctx.Value(mcpRelayChildKey{}).(bool); !remote {
		return false
	}
	switch name {
	case "bash", "code_execution", "read", "write", "edit", "replace_lines", "insert_lines", "file_search", "content_search", "glob", "find_files", "grep", "filter", "man":
		return true
	}
	return false
}

func (s *Server) validateProxyCLIConfinement(provider config.ProviderType, relayed bool) error {
	if (config.IsClaudeProviderRef(string(provider)) || provider == config.ProviderCursor) && !relayed {
		return errors.New("CLI providers require an authenticated child MCP relay")
	}
	if provider == config.ProviderKimiCLI {
		return fmt.Errorf("provider %s does not support isolated child MCP relay", provider)
	}
	if config.IsFallbackAggregateRef(string(provider)) || provider == config.ProviderFallback {
		chain, err := s.fallbackNodesForProvider(provider)
		if err != nil {
			return err
		}
		for _, node := range chain {
			if (!relayed && (config.IsClaudeProviderRef(node.Provider) || node.Provider == string(config.ProviderCursor))) || node.Provider == string(config.ProviderKimiCLI) {
				return fmt.Errorf("fallback provider %s does not support isolated child MCP relay", node.Provider)
			}
		}
	}
	return nil
}
