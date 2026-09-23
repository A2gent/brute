package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
)

const (
	defaultBaseURL = "https://api.typesafe.ai/v1"
	defaultModel   = "jev-latest"
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

type systemOneRequest struct {
	State     string                `json:"state"`
	Model     string                `json:"model,omitempty"`
	Questions map[string]systemOneQ `json:"questions"`
}

type systemOneQ struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type systemOneResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]systemOneAnswer `json:"answers"`
	Usage   systemOneUsage             `json:"usage"`
}

type systemOneAnswer struct {
	Type       string  `json:"type"`
	Choice     string  `json:"choice,omitempty"`
	Noul       float64 `json:"noul,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

type systemOneUsage struct {
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
		return c.classifyRoute(ctx, model, request.SystemPrompt, userPrompt, criteria)
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

func (c *Client) classifyRoute(ctx context.Context, model, instructions, userPrompt string, criteria map[string]string) (*llm.ChatResponse, error) {
	if strings.TrimSpace(instructions) == "" {
		instructions = "Choose the routing rule that best matches the user's primary requested action. Classify by deliverable and action, not incidental words."
	}
	resp, err := c.systemOne(ctx, systemOneRequest{
		State: userPrompt,
		Model: model,
		Questions: map[string]systemOneQ{
			"route": {
				Type:         "choice",
				Instructions: instructions,
				Criteria:     criteria,
			},
		},
	})
	if err != nil {
		return nil, err
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
	resp, err := c.systemOne(ctx, systemOneRequest{
		State: state,
		Model: model,
		Questions: map[string]systemOneQ{
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
		Content: fmt.Sprintf("connected (noul=%.2f)", answer.Noul),
		Usage: llm.TokenUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}, nil
}

func (c *Client) systemOne(ctx context.Context, payload systemOneRequest) (*systemOneResponse, error) {
	if strings.TrimSpace(c.apiKey) == "" {
		return nil, llm.UnsafeForRetry(fmt.Errorf("jev requires an API key"))
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

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jev request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("jev request failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusUnprocessableEntity {
			return nil, llm.UnsafeForRetry(err)
		}
		return nil, err
	}

	var parsed systemOneResponse
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
