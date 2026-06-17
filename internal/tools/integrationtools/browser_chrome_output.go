package integrationtools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// formatElementsAsTOON formats the interactive elements result in TOON format
// TOON (Token-Oriented Object Notation) is a compact, LLM-friendly format
// that reduces token usage by ~40% compared to JSON
// See: https://github.com/toon-format/toon
func formatElementsAsTOON(result interface{}) string {
	data, ok := result.(map[string]interface{})
	if !ok {
		return fmt.Sprintf("%v", result)
	}

	elements, ok := data["elements"].([]interface{})
	if !ok {
		return fmt.Sprintf("%v", result)
	}

	total := int(data["total"].(float64))
	pageNum := int(data["page"].(float64))
	perPage := int(data["perPage"].(float64))
	hasMore := data["hasMore"].(bool)

	var sb strings.Builder

	// TOON header with metadata
	sb.WriteString(fmt.Sprintf("total: %d\n", total))
	sb.WriteString(fmt.Sprintf("page: %d\n", pageNum))
	sb.WriteString(fmt.Sprintf("perPage: %d\n", perPage))
	sb.WriteString(fmt.Sprintf("hasMore: %v\n", hasMore))

	// TOON tabular array format: key[N]{field1,field2,...}:
	sb.WriteString(fmt.Sprintf("elements[%d]{selector,tag,role,type,text,x,y,w,h,state,href}:\n", len(elements)))

	for _, elem := range elements {
		el, ok := elem.(map[string]interface{})
		if !ok {
			continue
		}

		selector := escapeField(getString(el, "selector"))
		tag := getString(el, "tag")
		role := getString(el, "role")
		typ := getString(el, "type")
		text := escapeField(getString(el, "text"))
		x := formatNumberField(el, "x")
		y := formatNumberField(el, "y")
		w := formatNumberField(el, "w")
		h := formatNumberField(el, "h")
		state := escapeField(getString(el, "state"))
		href := getString(el, "href")

		// TOON row format: value1,value2,value3,...
		sb.WriteString(fmt.Sprintf("  %s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n", selector, tag, role, typ, text, x, y, w, h, state, href))
	}

	return sb.String()
}

// getString safely extracts a string from a map
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func formatNumberField(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok && v != nil {
		switch n := v.(type) {
		case float64:
			return fmt.Sprintf("%.0f", n)
		case float32:
			return fmt.Sprintf("%.0f", n)
		case int:
			return fmt.Sprintf("%d", n)
		case int64:
			return fmt.Sprintf("%d", n)
		case json.Number:
			return n.String()
		}
	}
	return ""
}

func buildBrowserEvalScript(script string) string {
	encoded, err := json.Marshal(script)
	if err != nil {
		encoded = []byte(`""`)
	}
	return fmt.Sprintf(`() => {
		const __script = %s;
		try {
			return (0, eval)(__script);
		} catch (__exprErr) {
			try {
				return (new Function(__script))();
			} catch (__stmtErr) {
				return {
					error: String(__stmtErr && __stmtErr.message ? __stmtErr.message : __stmtErr),
					expression_error: String(__exprErr && __exprErr.message ? __exprErr.message : __exprErr)
				};
			}
		}
	}`, string(encoded))
}

// escapeField escapes a TOON field value if it contains special characters
// Per TOON spec: quote with " if value contains , or newline; escape " as ""
func escapeField(s string) string {
	if s == "" {
		return s
	}
	needsQuoting := strings.ContainsAny(s, ",\n\r\"")
	if !needsQuoting {
		return s
	}
	// Escape internal quotes by doubling them
	escaped := strings.ReplaceAll(s, "\"", "\"\"")
	return "\"" + escaped + "\""
}
