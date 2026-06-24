package http

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path"
	"strings"
)

const (
	maxTunnelBruteHTTPBodyBytes = 2 * 1024 * 1024
	bruteHTTPInternalEvent      = "brute_http_request"
)

type bruteHTTPProxyEnvelope struct {
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Task     string                 `json:"task,omitempty"`
	HTTP     bruteHTTPProxyRequest  `json:"http"`
}

type bruteHTTPProxyRequest struct {
	Method      string              `json:"method"`
	Path        string              `json:"path"`
	RawQuery    string              `json:"raw_query,omitempty"`
	Headers     map[string][]string `json:"headers,omitempty"`
	ContentType string              `json:"content_type,omitempty"`
	BodyBase64  string              `json:"body_base64,omitempty"`
}

type bruteHTTPProxyResponseEnvelope struct {
	HTTP bruteHTTPProxyResponse `json:"http"`
}

type bruteHTTPProxyResponse struct {
	StatusCode  int                 `json:"status_code"`
	Headers     map[string][]string `json:"headers,omitempty"`
	ContentType string              `json:"content_type,omitempty"`
	BodyBase64  string              `json:"body_base64,omitempty"`
}

func (s *Server) handleBruteHTTPInternalEvent(_ context.Context, payload json.RawMessage) ([]byte, string, error) {
	var envelope bruteHTTPProxyEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, "", fmt.Errorf("failed to decode brute HTTP proxy envelope: %w", err)
	}

	method, err := sanitizeBruteHTTPMethod(envelope.HTTP.Method)
	if err != nil {
		return nil, "", err
	}
	proxiedPath, err := sanitizeBruteHTTPPath(envelope.HTTP.Path)
	if err != nil {
		return nil, "", err
	}
	rawQuery, err := sanitizeBruteHTTPRawQuery(envelope.HTTP.RawQuery)
	if err != nil {
		return nil, "", err
	}

	body, err := decodeBruteHTTPBody(envelope.HTTP.BodyBase64)
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxTunnelBruteHTTPBodyBytes {
		return nil, "", fmt.Errorf("proxied request body too large")
	}

	target := proxiedPath
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	for name, values := range sanitizeBruteHTTPRequestHeaders(envelope.HTTP.Headers) {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if contentType := strings.TrimSpace(envelope.HTTP.ContentType); contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(res.Body, maxTunnelBruteHTTPBodyBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read proxied response body")
	}
	if len(responseBody) > maxTunnelBruteHTTPBodyBytes {
		return nil, "", fmt.Errorf("proxied response body too large")
	}

	responsePayload, err := json.Marshal(bruteHTTPProxyResponseEnvelope{
		HTTP: bruteHTTPProxyResponse{
			StatusCode:  res.StatusCode,
			Headers:     sanitizeBruteHTTPResponseHeaders(res.Header),
			ContentType: strings.TrimSpace(res.Header.Get("Content-Type")),
			BodyBase64:  base64.StdEncoding.EncodeToString(responseBody),
		},
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to encode brute HTTP proxy response: %w", err)
	}
	return responsePayload, "", nil
}

func decodeBruteHTTPBody(raw string) ([]byte, error) {
	encoded := strings.TrimSpace(raw)
	if encoded == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid proxied request body")
	}
	return decoded, nil
}

func sanitizeBruteHTTPMethod(method string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(method))
	switch normalized {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		return normalized, nil
	default:
		return "", fmt.Errorf("unsupported proxied method")
	}
}

func sanitizeBruteHTTPPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, `\`) || strings.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("invalid brute path")
	}
	if raw == "" {
		return "/", nil
	}
	segments := strings.Split(raw, "/")
	cleanSegments := make([]string, 0, len(segments))
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" || segment == "." {
			continue
		}
		if segment == ".." {
			return "", fmt.Errorf("invalid brute path")
		}
		cleanSegments = append(cleanSegments, segment)
	}
	cleaned := path.Clean("/" + strings.Join(cleanSegments, "/"))
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	return cleaned, nil
}

func sanitizeBruteHTTPRawQuery(raw string) (string, error) {
	if strings.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("invalid brute query")
	}
	return raw, nil
}

func sanitizeBruteHTTPRequestHeaders(headers map[string][]string) map[string][]string {
	return sanitizeBruteHTTPHeaders(headers, isBlockedBruteHTTPRequestHeader)
}

func sanitizeBruteHTTPResponseHeaders(headers http.Header) map[string][]string {
	return sanitizeBruteHTTPHeaders(headers, isBlockedBruteHTTPResponseHeader)
}

func sanitizeBruteHTTPHeaders(headers map[string][]string, blocked func(string) bool) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string][]string, len(headers))
	for name, values := range headers {
		canonicalName := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
		if !isProxySafeHeaderName(canonicalName) || blocked(canonicalName) {
			continue
		}
		cleanValues := make([]string, 0, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" || strings.ContainsAny(value, "\r\n") {
				continue
			}
			if len(value) > 8*1024 {
				value = value[:8*1024]
			}
			cleanValues = append(cleanValues, value)
		}
		if len(cleanValues) > 0 {
			out[canonicalName] = cleanValues
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isBlockedBruteHTTPRequestHeader(name string) bool {
	switch name {
	case "Authorization", "Cookie", "Host", "Content-Length", "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "X-Api-Key", "Origin", "Referer":
		return true
	}
	return strings.HasPrefix(name, "Sec-") || strings.HasPrefix(name, "X-Forwarded-") || name == "X-Real-Ip"
}

func isBlockedBruteHTTPResponseHeader(name string) bool {
	switch name {
	case "Set-Cookie", "Content-Length", "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}

func isProxySafeHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			continue
		}
		switch ch {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}
