package jev

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const (
	defaultBaseURL = "https://api.typesafe.ai/v1"
	// Pinned instead of jev-latest so confidence behavior changes only on purpose.
	defaultModel = "jev-1.14.0"
	// Routing reads the intent, so a short state is enough and avoids max_tokens_exceeded.
	maxRouteAttempts = 4
	minRouteState    = 2 * 1024
)

// Client talks to TypeSafe System One. Jev is a routing classifier, not a coding LLM.
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

func NewClient(apiKey, model, baseURL string) *Client {
	if strings.TrimSpace(model) == "" {
		model = defaultModel
	}
	return &Client{
		apiKey:  strings.TrimSpace(apiKey),
		baseURL: NormalizeBaseURL(baseURL),
		model:   strings.TrimSpace(model),
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// NormalizeBaseURL keeps the TypeSafe /v1 root and strips accidental endpoint suffixes.
func NormalizeBaseURL(raw string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(raw), "/")
	if baseURL == "" {
		return defaultBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/systemone")
	baseURL = strings.TrimSuffix(baseURL, "/models")
	return strings.TrimRight(baseURL, "/")
}

// State and Instructions are `any` because System One accepts structured values, not just text:
// browser_act sends an object state (page + element table) and object instructions (goal + rules).
// Existing string callers are unaffected - a string in an `any` field marshals identically.
type SystemOneRequest struct {
	State     any                 `json:"state"`
	Model     string              `json:"model,omitempty"`
	Questions map[string]Question `json:"questions"`
	// Assets holds images keyed by the reference string used in state/instructions.
	Assets map[string]Asset `json:"assets,omitempty"`
}

// StatusError is a non-2xx System One response; callers branch on StatusCode/Body.
type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("jev request failed: HTTP %d: %s", e.StatusCode, e.Body)
}

func statusCode(err error) int {
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode
	}
	return 0
}

func isMaxTokensExceeded(err error) bool {
	var statusErr *StatusError
	return errors.As(err, &statusErr) && strings.Contains(statusErr.Body, "max_tokens_exceeded")
}

type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// InstructionsString renders the instructions as text, for the same reason as StateString.
func (q Question) InstructionsString() string {
	if text, ok := q.Instructions.(string); ok {
		return text
	}
	raw, err := json.Marshal(q.Instructions)
	if err != nil {
		return ""
	}
	return string(raw)
}

// StateString renders the state as text. Text-state callers (classify, relevance_gate) and their
// tests use this instead of asserting on the field type, which structured senders also populate.
func (r SystemOneRequest) StateString() string {
	if text, ok := r.State.(string); ok {
		return text
	}
	raw, err := json.Marshal(r.State)
	if err != nil {
		return ""
	}
	return string(raw)
}

type SystemOneResponse struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type modelsResponse struct {
	Models []modelCard `json:"models"`
}

type modelCard struct {
	Name string `json:"name"`
}

type indexedRule struct {
	Index  int    `json:"index"`
	Match  string `json:"match"`
	Target string `json:"target"`
}

func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("chat request is nil")
	}
	if len(request.Tools) > 0 {
		return nil, llm.UnsafeForRetry(fmt.Errorf("jev is a routing classifier and does not support tool-calling chat completions"))
	}

	userContent := collectUserContent(request)
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = c.model
	}

	if userPrompt, criteria, ok := parseRouterUserMessage(userContent); ok {
		return c.classifyRoute(ctx, model, request.SystemPrompt, userPrompt, criteria, collectImages(request))
	}

	return c.connectivityCheck(ctx, model, userContent)
}

func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	c.applyAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jev list models request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("jev list models failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed modelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("jev list models: invalid response: %w", err)
	}

	seen := map[string]struct{}{}
	out := make([]string, 0, len(parsed.Models)+1)
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	add(defaultModel)
	for _, model := range parsed.Models {
		add(model.Name)
	}
	return out, nil
}

func (c *Client) classifyRoute(ctx context.Context, model, instructions, userPrompt string, criteria map[string]string, images []llm.Image) (*llm.ChatResponse, error) {
	if strings.TrimSpace(instructions) == "" {
		instructions = "Choose the routing rule that best matches the user's primary requested action. Classify by deliverable and action, not incidental words."
	}
	refs, assets := routerAssets(images)
	var resp *SystemOneResponse
	for attempt := 1; ; attempt++ {
		var err error
		resp, err = c.SystemOne(ctx, SystemOneRequest{
			State: routeState(userPrompt, refs),
			Model: model,
			Questions: map[string]Question{
				"route": {
					Type:         "choice",
					Instructions: instructions,
					Criteria:     criteria,
				},
			},
			Assets: assets,
		})
		if err == nil {
			break
		}
		if attempt >= maxRouteAttempts {
			return nil, err
		}
		// Images are an experimental Jev feature and cost context: drop them first,
		// then halve the text, so an oversized prompt still gets routed.
		code := statusCode(err)
		switch {
		case len(assets) > 0 && (code == http.StatusBadRequest || code == http.StatusRequestEntityTooLarge):
			logging.Warn("Jev router rejected images, retrying text-only: %v", err)
			refs, assets = nil, nil
		case isMaxTokensExceeded(err) && len(userPrompt) > minRouteState:
			userPrompt = llm.HeadTail(userPrompt, len(userPrompt)/2)
			logging.Warn("Jev router prompt exceeded context, retrying with %d bytes", len(userPrompt))
		default:
			return nil, err
		}
	}
	answer, ok := resp.Answers["route"]
	if !ok || strings.TrimSpace(answer.Choice) == "" {
		return nil, fmt.Errorf("jev returned no routing choice")
	}
	index, err := strconv.Atoi(strings.TrimSpace(answer.Choice))
	if err != nil {
		return nil, fmt.Errorf("jev returned non-numeric routing choice %q", answer.Choice)
	}
	reason := fmt.Sprintf("jev confidence=%.2f", answer.Confidence)
	payload, _ := json.Marshal(map[string]any{"index": index, "reason": reason})
	return &llm.ChatResponse{
		Content: string(payload),
		Usage: llm.TokenUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}, nil
}

func (c *Client) connectivityCheck(ctx context.Context, model, state string) (*llm.ChatResponse, error) {
	if strings.TrimSpace(state) == "" {
		state = "hello"
	}
	resp, err := c.SystemOne(ctx, SystemOneRequest{
		State: state,
		Model: model,
		Questions: map[string]Question{
			"ok": {
				Type:         "noul",
				Instructions: "Is this a greeting or a simple connectivity check?",
			},
		},
	})
	if err != nil {
		return nil, err
	}
	answer := resp.Answers["ok"]
	return &llm.ChatResponse{
		Content: fmt.Sprintf("connected (noul=%.2f)", noulValue(answer.Noul)),
		Usage: llm.TokenUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}, nil
}

// SystemOne calls the classifier directly without the chat router adapter.
func (c *Client) SystemOne(ctx context.Context, payload SystemOneRequest) (response *SystemOneResponse, resultErr error) {
	started := time.Now()
	defer func() { recordRequest(ctx, payload, response, resultErr, started) }()
	if strings.TrimSpace(c.apiKey) == "" {
		return nil, llm.UnsafeForRetry(fmt.Errorf("jev requires an API key"))
	}
	if strings.TrimSpace(payload.Model) == "" {
		payload.Model = c.model
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/systemone", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c.applyAuth(req)
	req.Header.Set("Content-Type", "application/json")
	if len(payload.Assets) > 0 {
		req.Header.Set(imagesHeader, "true")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jev request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := &StatusError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusUnprocessableEntity {
			return nil, llm.UnsafeForRetry(err)
		}
		return nil, err
	}

	var parsed SystemOneResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("jev returned invalid JSON: %w", err)
	}
	return &parsed, nil
}

func (c *Client) applyAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
}

func collectUserContent(request *llm.ChatRequest) string {
	parts := make([]string, 0, len(request.Messages))
	for _, msg := range request.Messages {
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		parts = append(parts, msg.Content)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// collectImages returns inline images of the latest user message that has any.
func collectImages(request *llm.ChatRequest) []llm.Image {
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if msg := request.Messages[i]; strings.EqualFold(msg.Role, "user") && len(msg.Images) > 0 {
			return msg.Images
		}
	}
	return nil
}

// routerAssets converts attachments into Jev assets; images Jev cannot take are skipped
// so a screenshot in an unusual format never blocks routing.
func routerAssets(images []llm.Image) ([]string, map[string]Asset) {
	var refs []string
	assets := map[string]Asset{}
	for _, img := range images {
		if len(refs) >= routerMaxImage {
			break
		}
		asset, err := prepareImage(img)
		if err != nil {
			logging.Warn("Jev router skipped image %q: %v", img.Name, err)
			continue
		}
		// References are matched as plain text anywhere in the request, so make them unguessable.
		ref := "img-" + randomHex()
		refs = append(refs, ref)
		assets[ref] = asset
	}
	if len(assets) == 0 {
		return nil, nil
	}
	return refs, assets
}

func randomHex() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// routeState keeps the plain-string state for text-only prompts and adds image refs otherwise.
func routeState(userPrompt string, refs []string) any {
	if len(refs) == 0 {
		return userPrompt
	}
	return map[string]any{"prompt": userPrompt, "attached_images": refs}
}

func parseRouterUserMessage(content string) (string, map[string]string, bool) {
	trimmed := strings.TrimSpace(content)
	idx := strings.Index(trimmed, "Rules:")
	if idx < 0 {
		return "", nil, false
	}
	rest := strings.TrimSpace(trimmed[idx+len("Rules:"):])
	promptIdx := strings.LastIndex(rest, "User prompt:")
	if promptIdx < 0 {
		return "", nil, false
	}
	rawRules := strings.TrimSpace(rest[:promptIdx])
	userPrompt := strings.TrimSpace(rest[promptIdx+len("User prompt:"):])

	var rules []indexedRule
	if err := json.Unmarshal([]byte(rawRules), &rules); err != nil || len(rules) == 0 {
		return "", nil, false
	}

	criteria := make(map[string]string, len(rules))
	for _, rule := range rules {
		key := strconv.Itoa(rule.Index)
		if rule.Index < 1 || strings.TrimSpace(key) == "" {
			continue
		}
		desc := strings.TrimSpace(rule.Match)
		if target := strings.TrimSpace(rule.Target); target != "" {
			if desc != "" {
				desc = desc + " -> " + target
			} else {
				desc = target
			}
		}
		if desc == "" {
			desc = "rule " + key
		}
		criteria[key] = desc
	}
	if len(criteria) == 0 {
		return "", nil, false
	}
	return userPrompt, criteria, true
}

var _ llm.Client = (*Client)(nil)

func noulValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
