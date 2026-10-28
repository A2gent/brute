package dbtool

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// WHY: Elasticsearch has no SQL tables, so the explorer sends raw REST requests.
// Keeping parsing, read-only validation and row shaping here mirrors the Redis layout.
const (
	// Elasticsearch's index.max_result_window default: deeper from+size paging is rejected.
	elasticsearchMaxHitsScan = 10000
)

// elasticsearchReadEndpoints lists path segments that only read data. A read-only
// connection may POST to them, but to nothing else.
//
// WHY an allowlist instead of a denylist: a denylist of write endpoints (`_bulk`,
// `_update`, ...) silently loses every new mutating Elasticsearch API (`_aliases`
// can delete indices, `_cluster/settings`, `_ilm`, `_snapshot/_restore`, ...).
var elasticsearchReadEndpoints = map[string]struct{}{
	"_search":        {},
	"_count":         {},
	"_msearch":       {},
	"_mget":          {},
	"_field_caps":    {},
	"_explain":       {},
	"_validate":      {},
	"_analyze":       {},
	"_search_shards": {},
	"_resolve":       {},
	"_terms_enum":    {},
	"_rank_eval":     {},
}

// elasticsearchRequest is a parsed `METHOD path [json body]` request.
type elasticsearchRequest struct {
	method string
	path   string
	body   []byte
}

// executeElasticsearchQuery runs one REST request and renders the response as rows.
func executeElasticsearchQuery(ctx context.Context, cfg Config, query string, limit, offset int, format string) (string, error) {
	request, err := parseElasticsearchRequest(query)
	if err != nil {
		return "", err
	}
	if cfg.IsReadOnly {
		if err := ensureElasticsearchReadOnlyRequest(request); err != nil {
			return "", err
		}
	}

	conn, err := parseElasticsearchDSN(cfg.DSN)
	if err != nil {
		return "", fmt.Errorf("failed to connect: %w", err)
	}

	request, err = withElasticsearchPagination(request, limit, offset)
	if err != nil {
		return "", err
	}

	data, err := conn.do(ctx, request.method, request.path, request.body)
	if err != nil {
		return "", err
	}

	return formatElasticsearchRows(elasticsearchResponseRows(data), format)
}

// parseElasticsearchRequest splits `GET logs/_search {"size":1}` into method, path and body.
// A body-only query searches every index, and a bare path defaults to GET.
func parseElasticsearchRequest(query string) (elasticsearchRequest, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return elasticsearchRequest{}, fmt.Errorf("elasticsearch request is required")
	}

	if strings.HasPrefix(trimmed, "{") {
		if err := validateElasticsearchJSONObject([]byte(trimmed)); err != nil {
			return elasticsearchRequest{}, err
		}
		return elasticsearchRequest{method: http.MethodGet, path: "_search", body: []byte(trimmed)}, nil
	}

	tokens := elasticsearchTokens(trimmed, 2)
	method := http.MethodGet
	pathToken := tokens[0]
	if isElasticsearchHTTPMethod(pathToken.text) {
		method = strings.ToUpper(pathToken.text)
		if len(tokens) < 2 {
			return elasticsearchRequest{}, fmt.Errorf("elasticsearch request needs a path, for example: GET logs/_search {}")
		}
		pathToken = tokens[1]
	}

	request := elasticsearchRequest{method: method, path: pathToken.text}
	// The body starts right after the path token so JSON whitespace stays untouched.
	body := strings.TrimSpace(trimmed[pathToken.end:])
	if body != "" {
		if err := validateElasticsearchJSONObject([]byte(body)); err != nil {
			return elasticsearchRequest{}, err
		}
		request.body = []byte(body)
	}
	return request, nil
}

// validateElasticsearchJSONObject rejects bodies that are not a JSON object.
// WHY: `null` and `[1,2]` are valid JSON but not search bodies, and `null`
// unmarshals to a nil map that would panic when paging fields are injected.
func validateElasticsearchJSONObject(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if !json.Valid(trimmed) {
		return fmt.Errorf("request body is not valid JSON")
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("request body must be a JSON object, for example: {\"query\":{\"match_all\":{}}}")
	}
	return nil
}

type elasticsearchToken struct {
	text       string
	start, end int
}

// elasticsearchTokens returns up to limit whitespace-separated tokens with their offsets.
func elasticsearchTokens(query string, limit int) []elasticsearchToken {
	tokens := make([]elasticsearchToken, 0, limit)
	position := 0
	for position < len(query) && len(tokens) < limit {
		for position < len(query) && (query[position] == ' ' || query[position] == '\t' || query[position] == '\n' || query[position] == '\r') {
			position++
		}
		start := position
		for position < len(query) && query[position] != ' ' && query[position] != '\t' && query[position] != '\n' && query[position] != '\r' {
			position++
		}
		if start == position {
			break
		}
		tokens = append(tokens, elasticsearchToken{text: query[start:position], start: start, end: position})
	}
	if len(tokens) == 0 {
		tokens = append(tokens, elasticsearchToken{text: query, end: len(query)})
	}
	return tokens
}

func isElasticsearchHTTPMethod(value string) bool {
	switch strings.ToUpper(value) {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead:
		return true
	default:
		return false
	}
}

// ensureElasticsearchReadOnlyRequest rejects anything that could mutate the cluster.
//
// GET and HEAD are read-only in Elasticsearch, and every mutating API needs
// POST/PUT/PATCH/DELETE. For POST the path must contain one of the read endpoints,
// so `POST _bulk`, `POST _aliases` or `POST logs/_doc/1` are refused.
func ensureElasticsearchReadOnlyRequest(request elasticsearchRequest) error {
	switch request.method {
	case http.MethodGet, http.MethodHead:
		return nil
	case http.MethodPost:
		for _, segment := range elasticsearchPathSegments(request.path) {
			if _, ok := elasticsearchReadEndpoints[segment]; ok {
				return nil
			}
		}
		return fmt.Errorf("only read endpoints (%s) may be called on a read-only connection", elasticsearchReadEndpointList())
	default:
		return fmt.Errorf("elasticsearch %s requests are not allowed on a read-only connection", request.method)
	}
}

func elasticsearchReadEndpointList() string {
	names := make([]string, 0, len(elasticsearchReadEndpoints))
	for name := range elasticsearchReadEndpoints {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// elasticsearchPathSegments splits the path part of a request into decoded segments.
//
// WHY decode: Elasticsearch decodes path segments before routing, so `%5Fbulk`
// reaches `/_bulk`. Validating the raw text would let that through, as would an
// encoded slash (`logs/_doc%2F1`) or a `#` fragment the HTTP client strips.
func elasticsearchPathSegments(path string) []string {
	// A fragment is dropped from the wire URL, so `_bulk#x` must be judged as `_bulk`.
	if index := strings.IndexByte(path, '#'); index >= 0 {
		path = path[:index]
	}
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	segments := make([]string, 0, 4)
	for _, segment := range strings.Split(path, "/") {
		if segment == "" {
			continue
		}
		// WHY: an invalid escape cannot be routed meaningfully; keeping the raw text
		// means it simply fails the read-endpoint check instead of being rewritten.
		if decoded, err := url.PathUnescape(segment); err == nil {
			segment = decoded
		}
		segments = append(segments, segment)
	}
	return segments
}

// withElasticsearchPagination injects `from`/`size` into _search bodies so the explorer
// pages results the same way it does for SQL tables. An explicit `size` in the request wins,
// which keeps aggregation requests such as {"size":0,"aggs":{...}} intact.
func withElasticsearchPagination(request elasticsearchRequest, limit, offset int) (elasticsearchRequest, error) {
	// WHY: only plain `<index>/_search` bodies accept size/from. Related endpoints such as
	// `_search/template`, `_msearch`, or `_search/scroll` take a different body shape, so
	// injecting paging fields there would turn a valid request into an Elasticsearch error.
	if !isPlainElasticsearchSearchPath(request.path) || limit <= 0 {
		return request, nil
	}
	if limit > elasticsearchMaxHitsScan {
		limit = elasticsearchMaxHitsScan
	}
	if offset < 0 {
		offset = 0
	}
	// WHY: Elasticsearch rejects from+size beyond index.max_result_window, so the last
	// page slides back instead of failing when the explorer walks past 10k documents.
	if offset > elasticsearchMaxHitsScan-limit {
		offset = elasticsearchMaxHitsScan - limit
	}

	// RawMessage keeps numeric fields such as 64-bit ids byte-identical; decoding into
	// map[string]any would round them through float64 and corrupt term queries.
	body := map[string]json.RawMessage{}
	if len(request.body) > 0 {
		if err := json.Unmarshal(request.body, &body); err != nil {
			return request, fmt.Errorf("request body must be a JSON object: %w", err)
		}
	}
	if body == nil {
		body = map[string]json.RawMessage{}
	}
	if _, hasSize := body["size"]; !hasSize {
		body["size"] = json.RawMessage(strconv.Itoa(limit))
	}
	if _, hasFrom := body["from"]; !hasFrom {
		body["from"] = json.RawMessage(strconv.Itoa(offset))
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return request, err
	}
	return elasticsearchRequest{method: http.MethodPost, path: request.path, body: encoded}, nil
}

// isPlainElasticsearchSearchPath reports whether the path ends with a whole `_search`
// segment, without a query string (which often carries `scroll`) and without a
// sub-resource such as `_search/template`.
func isPlainElasticsearchSearchPath(path string) bool {
	if strings.ContainsAny(path, "?#") {
		return false
	}
	segments := elasticsearchPathSegments(path)
	if len(segments) == 0 {
		return false
	}
	return segments[len(segments)-1] == "_search"
}

// elasticsearchResponseRows turns a JSON response body into flat rows for the grid.
func elasticsearchResponseRows(data []byte) []map[string]interface{} {
	// WHY UseNumber: hit values can hold 64-bit ids and nanosecond timestamps, which
	// float64 silently rounds.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return []map[string]interface{}{{"response": string(data)}}
	}

	object, isObject := payload.(map[string]any)
	if isObject {
		if hits, ok := object["hits"].(map[string]any); ok {
			rows := elasticsearchHitRows(hits)
			// WHY: a _search response always carries hits, so an aggregation result would
			// be invisible otherwise. Append it as its own row for size:0 queries.
			if aggregations, ok := object["aggregations"].(map[string]any); ok {
				aggregateRow := map[string]interface{}{}
				flattenElasticsearchValue(aggregateRow, "", aggregations, 0)
				rows = append(rows, aggregateRow)
			}
			return rows
		}
		// WHY: _count answers {"count":N,"_shards":{...}}. Flattening _shards would put
		// "_shards.failed" (0) before "count", and the UI reads the first numeric cell
		// as the row total, so keep only the count here.
		if count, ok := object["count"]; ok && object["_shards"] != nil {
			return []map[string]interface{}{{"count": count}}
		}
		if aggregations, ok := object["aggregations"].(map[string]any); ok {
			row := map[string]interface{}{}
			flattenElasticsearchValue(row, "", aggregations, 0)
			return []map[string]interface{}{row}
		}
	}

	if items, ok := payload.([]any); ok {
		rows := make([]map[string]interface{}, 0, len(items))
		for _, item := range items {
			row := map[string]interface{}{}
			flattenElasticsearchValue(row, "", item, 0)
			rows = append(rows, row)
		}
		return rows
	}

	row := map[string]interface{}{}
	flattenElasticsearchValue(row, "", payload, 0)
	return []map[string]interface{}{row}
}

func elasticsearchHitRows(hits map[string]any) []map[string]interface{} {
	entries, _ := hits["hits"].([]any)
	rows := make([]map[string]interface{}, 0, len(entries))
	for _, entry := range entries {
		hit, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		row := map[string]interface{}{}
		// WHY source first: a document with its own `_id` field must not shadow the
		// metadata column the explorer shows first.
		if source, ok := hit["_source"].(map[string]any); ok {
			flattenElasticsearchValue(row, "", source, 0)
		} else {
			flattenElasticsearchValue(row, "", hit, 0)
		}
		if value, ok := hit["_id"]; ok {
			row["_id"] = value
		}
		if value, ok := hit["_index"]; ok {
			row["_index"] = value
		}
		rows = append(rows, row)
	}
	return rows
}

// flattenElasticsearchValue expands nested objects into dot-notation columns and renders
// arrays as JSON text, which keeps every cell a single grid value.
func flattenElasticsearchValue(row map[string]interface{}, prefix string, value any, depth int) {
	switch typed := value.(type) {
	case map[string]any:
		if depth >= elasticsearchMaxFlattenDepth {
			row[orElasticsearchFlatKey(prefix)] = jsonText(typed)
			return
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			next := key
			if prefix != "" {
				next = prefix + "." + key
			}
			flattenElasticsearchValue(row, next, typed[key], depth+1)
		}
	case []any:
		row[orElasticsearchFlatKey(prefix)] = jsonText(typed)
	default:
		row[orElasticsearchFlatKey(prefix)] = typed
	}
}

func orElasticsearchFlatKey(key string) string {
	if key == "" {
		return "value"
	}
	return key
}

func jsonText(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

// formatElasticsearchRows renders rows as CSV or JSON, mirroring the Redis formatter.
func formatElasticsearchRows(rows []map[string]interface{}, format string) (string, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		// Default matches the SQL and Redis paths.
		format = "csv"
	}
	if format == "json" {
		encoded, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
	if format != "csv" {
		return "", fmt.Errorf("unsupported format: %s", format)
	}

	columns := elasticsearchColumns(rows)
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if err := writer.Write(columns); err != nil {
		return "", err
	}
	for _, row := range rows {
		values := make([]string, len(columns))
		for index, column := range columns {
			values[index] = elasticsearchCellString(row[column])
		}
		if err := writer.Write(values); err != nil {
			return "", err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// elasticsearchColumns keeps metadata columns first and then source fields in a stable order.
func elasticsearchColumns(rows []map[string]interface{}) []string {
	seen := map[string]struct{}{}
	for _, row := range rows {
		for column := range row {
			seen[column] = struct{}{}
		}
	}

	columns := make([]string, 0, len(seen))
	for _, preferred := range []string{"_id", "_index"} {
		if _, ok := seen[preferred]; ok {
			columns = append(columns, preferred)
			delete(seen, preferred)
		}
	}
	rest := make([]string, 0, len(seen))
	for column := range seen {
		rest = append(rest, column)
	}
	sort.Strings(rest)
	return append(columns, rest...)
}

func elasticsearchCellString(value interface{}) string {
	if value == nil {
		return "NULL"
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		// Printed verbatim so 64-bit ids and large timestamps keep every digit.
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return jsonText(typed)
	}
}
