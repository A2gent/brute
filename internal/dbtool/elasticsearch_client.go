package dbtool

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// WHY: Elasticsearch has no SQL driver, so it gets its own small HTTP/JSON client
// next to the Redis wire client. The explorer treats indices as "tables" and the
// query bar accepts raw Elasticsearch requests (e.g. `GET logs/_search {"query":{}}`).
const (
	elasticsearchDefaultPort      = "9200"
	elasticsearchRequestTimeout   = 20 * time.Second
	elasticsearchMaxResponseBytes = 32 << 20
	// Nested documents are flattened this deep before they become JSON text columns.
	elasticsearchMaxFlattenDepth = 3
)

type elasticsearchConn struct {
	baseURL  string
	username string
	password string
	apiKey   string
	insecure bool
	client   *http.Client
}

// parseElasticsearchDSN accepts `http(s)://user:pass@host:9200/prefix?api_key=..&insecure=true`,
// the friendlier `elasticsearch://` alias for http, and a bare `host:9200`.
func parseElasticsearchDSN(dsn string) (elasticsearchConn, error) {
	text := strings.TrimSpace(dsn)
	if text == "" {
		return elasticsearchConn{}, fmt.Errorf("elasticsearch DSN is required")
	}
	if !strings.Contains(text, "://") {
		text = "http://" + text
	}

	parsed, err := url.Parse(text)
	if err != nil {
		// WHY: url.Parse errors echo the raw URL, which contains the password. Return a
		// generic message so the DSN never reaches the UI diagnostics or agent context.
		return elasticsearchConn{}, fmt.Errorf("invalid elasticsearch DSN: check the host, port and percent-encoding")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "elasticsearch" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return elasticsearchConn{}, fmt.Errorf("unsupported elasticsearch DSN scheme: %s", parsed.Scheme)
	}

	host := parsed.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	port := parsed.Port()
	if port == "" {
		port = elasticsearchDefaultPort
	}

	query := parsed.Query()
	conn := elasticsearchConn{
		baseURL:  scheme + "://" + net.JoinHostPort(host, port) + strings.TrimSuffix(parsed.Path, "/"),
		apiKey:   firstNonEmptyString(query.Get("api_key"), query.Get("apikey")),
		insecure: query.Get("insecure") == "true" || query.Get("insecure") == "1",
	}
	if parsed.User != nil {
		conn.username = parsed.User.Username()
		conn.password, _ = parsed.User.Password()
	}
	// WHY: `http://:secret@host` carries a password without a user name; without this,
	// the password would be dropped and the cluster would answer "missing credentials".
	if conn.username == "" && conn.password != "" {
		conn.username = "elastic"
	}
	conn.client = newElasticsearchHTTPClient(conn.insecure)
	return conn, nil
}

func newElasticsearchHTTPClient(insecure bool) *http.Client {
	client := &http.Client{Timeout: elasticsearchRequestTimeout}
	if insecure {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// #nosec G402 - skipping verification is opt-in (?insecure=true) for self-signed clusters.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
		client.Transport = transport
	}
	return client
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// do sends one request relative to the cluster root and returns the raw JSON body.
func (c elasticsearchConn) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	endpoint := c.baseURL + "/" + strings.TrimPrefix(strings.TrimSpace(path), "/")
	var payload io.Reader
	if len(body) > 0 {
		payload = bytes.NewReader(body)
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "ApiKey "+c.apiKey)
	} else if c.username != "" {
		request.SetBasicAuth(c.username, c.password)
	}

	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	data, err := io.ReadAll(io.LimitReader(response.Body, elasticsearchMaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read elasticsearch response: %w", err)
	}
	// WHY: a silent truncation would feed half-parsed JSON back as a data column, which
	// overflows the model context instead of telling the user to narrow the query.
	if len(data) > elasticsearchMaxResponseBytes {
		return nil, fmt.Errorf("elasticsearch response exceeds %d bytes; narrow the query with filters or a smaller page", elasticsearchMaxResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("elasticsearch returned %s: %s", response.Status, elasticsearchErrorReason(data))
	}
	return data, nil
}

// elasticsearchErrorReason pulls the human-readable reason out of an error body.
func elasticsearchErrorReason(data []byte) string {
	var payload struct {
		Error struct {
			Reason    string `json:"reason"`
			RootCause []struct {
				Reason string `json:"reason"`
			} `json:"root_cause"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &payload); err == nil {
		if reason := strings.TrimSpace(payload.Error.Reason); reason != "" {
			return reason
		}
		if len(payload.Error.RootCause) > 0 && payload.Error.RootCause[0].Reason != "" {
			return payload.Error.RootCause[0].Reason
		}
		if payload.Error.Type != "" {
			return payload.Error.Type
		}
	}
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) > 300 {
		trimmed = trimmed[:300]
	}
	return trimmed
}

// connectElasticsearch verifies the cluster answers and returns a short description.
func connectElasticsearch(ctx context.Context, dsn string) (elasticsearchConn, string, error) {
	conn, err := parseElasticsearchDSN(dsn)
	if err != nil {
		return conn, "", err
	}

	data, err := conn.do(ctx, http.MethodGet, "/", nil)
	if err != nil {
		// WHY: GET / needs the cluster monitor privilege, which index-scoped API keys usually
		// lack. Fall back to the alias listing so "Test connection" reports what the explorer
		// can actually do, and still fails for a truly unreachable cluster.
		if _, listErr := listElasticsearchCollections(ctx, conn); listErr != nil {
			return conn, "", err
		}
		return conn, "reachable (no cluster monitor permission)", nil
	}

	var root struct {
		ClusterName string `json:"cluster_name"`
		Version     struct {
			Number string `json:"number"`
		} `json:"version"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return conn, "", fmt.Errorf("unexpected elasticsearch root response: %w", err)
	}

	detail := "cluster " + root.ClusterName
	if root.Version.Number != "" {
		detail += " v" + root.Version.Number
	}
	return conn, strings.TrimSpace(detail), nil
}

// listElasticsearchCollections returns index and alias names, which the explorer
// shows as selectable collections.
func listElasticsearchCollections(ctx context.Context, conn elasticsearchConn) ([]string, error) {
	data, err := conn.do(ctx, http.MethodGet, "_alias", nil)
	if err != nil {
		// WHY: API keys scoped to index `read` often cannot read the alias API. Fall back
		// to the index catalogue so the explorer still lists something useful.
		return listElasticsearchIndicesFallback(ctx, conn)
	}

	var aliasResponse map[string]struct {
		Aliases map[string]struct{} `json:"aliases"`
	}
	if err := json.Unmarshal(data, &aliasResponse); err != nil {
		return nil, fmt.Errorf("unexpected elasticsearch alias response: %w", err)
	}

	seen := make(map[string]struct{}, len(aliasResponse))
	for index, entry := range aliasResponse {
		// WHY: Dot-prefixed names are system indices (Kibana, ILM, ...). They are noise
		// in the explorer and usually not readable with data-only credentials.
		if !strings.HasPrefix(index, ".") {
			seen[index] = struct{}{}
		}
		for alias := range entry.Aliases {
			if !strings.HasPrefix(alias, ".") {
				seen[alias] = struct{}{}
			}
		}
	}

	return sortedElasticsearchNames(seen), nil
}

func listElasticsearchIndicesFallback(ctx context.Context, conn elasticsearchConn) ([]string, error) {
	data, err := conn.do(ctx, http.MethodGet, "_cat/indices?format=json&h=index&s=index", nil)
	if err != nil {
		return nil, err
	}

	var entries []struct {
		Index string `json:"index"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("unexpected elasticsearch indices response: %w", err)
	}

	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.Index != "" && !strings.HasPrefix(entry.Index, ".") {
			seen[entry.Index] = struct{}{}
		}
	}
	return sortedElasticsearchNames(seen), nil
}

func sortedElasticsearchNames(names map[string]struct{}) []string {
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
