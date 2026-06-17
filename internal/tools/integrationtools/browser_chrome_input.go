package integrationtools

import (
	"strings"

	"github.com/go-rod/rod/lib/input"
)

// escapeCSSSelector escapes special characters in CSS selectors
// React generates IDs like ":r1:" which need escaping as CSS doesn't allow unescaped colons in IDs
func escapeCSSSelector(selector string) string {
	// If selector starts with # (ID selector), escape special chars in the ID part
	if strings.HasPrefix(selector, "#") {
		id := selector[1:]
		// Escape colons and other special CSS characters in the ID
		var escaped strings.Builder
		escaped.WriteByte('#')
		for _, c := range id {
			// CSS special characters that need escaping: : . [ ] ( ) # > + ~ = ^ $ * | ! @ % &
			if c == ':' || c == '.' || c == '[' || c == ']' || c == '(' || c == ')' ||
				c == '#' || c == '>' || c == '+' || c == '~' || c == '=' || c == '^' ||
				c == '$' || c == '*' || c == '|' || c == '!' || c == '@' || c == '%' ||
				c == '&' || c == '/' || c == '\\' {
				escaped.WriteByte('\\')
			}
			escaped.WriteRune(c)
		}
		return escaped.String()
	}
	return selector
}

// keyFromString converts a key name string to input.Key
// Supports common key names like Enter, Escape, Tab, etc.
func keyFromString(key string) input.Key {
	switch strings.ToLower(key) {
	case "enter", "return":
		return input.Enter
	case "escape", "esc":
		return input.Escape
	case "tab":
		return input.Tab
	case "backspace":
		return input.Backspace
	case "delete":
		return input.Delete
	case "space":
		return input.Space
	case "arrowup", "up":
		return input.ArrowUp
	case "arrowdown", "down":
		return input.ArrowDown
	case "arrowleft", "left":
		return input.ArrowLeft
	case "arrowright", "right":
		return input.ArrowRight
	case "home":
		return input.Home
	case "end":
		return input.End
	case "pageup":
		return input.PageUp
	case "pagedown":
		return input.PageDown
	case "f1":
		return input.F1
	case "f2":
		return input.F2
	case "f3":
		return input.F3
	case "f4":
		return input.F4
	case "f5":
		return input.F5
	case "f6":
		return input.F6
	case "f7":
		return input.F7
	case "f8":
		return input.F8
	case "f9":
		return input.F9
	case "f10":
		return input.F10
	case "f11":
		return input.F11
	case "f12":
		return input.F12
	default:
		return 0
	}
}
