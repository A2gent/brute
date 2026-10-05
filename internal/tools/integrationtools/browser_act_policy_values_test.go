package integrationtools

import (
	"fmt"
	"strings"
	"testing"
)

func TestActPolicyControlValues(t *testing.T) {
	for _, checked := range []string{"false", "true"} {
		t.Run("checked="+checked, func(t *testing.T) {
			actions := []actAction{
				{Node: 1, Kind: "click", Role: "checkbox", Label: "Agree to terms", Value: "on", Checked: checked},
				{Node: 2, Kind: "click", Role: "button", Label: "Search"},
				{Node: 3, Kind: "click", Role: "button", Label: "Apply", Value: "submit-search"},
				{Node: 4, Kind: "fill", Role: "textbox", Label: "Search query"},
				{Node: 4, Kind: "click", Role: "textbox", Label: "Open Search query"},
				{Node: 5, Kind: "fill", Role: "textbox", Label: "Populated query", Value: "widgets"},
				{Node: 6, Kind: "select", Role: "combobox", Label: "Sort by -> Newest", Value: "new", CurrentValue: "Relevance"},
			}
			space := buildActSpace(actions)
			rows := strings.Split(strings.TrimSuffix(renderActElements(space, 20), "\n"), "\n")
			want := []string{
				fmt.Sprintf("  [1] checkbox   Agree to terms · checked=%s (CLICK)", checked),
				"  [2] button     Search (CLICK)",
				"  [3] button     Apply (CLICK)",
				"  [4] textbox    Search query · empty (TYPE_TEXT,CLICK)",
				"  [5] textbox    Populated query · widgets (TYPE_TEXT)",
				"  [6] combobox   Sort by · Relevance (SELECT)",
			}
			if len(rows) != len(want) {
				t.Fatalf("unexpected rows: %v", rows)
			}
			for i := range want {
				if rows[i] != want[i] {
					t.Errorf("row %d: got %q, want %q", i+1, rows[i], want[i])
				}
			}
			for _, element := range space.Elements[:3] {
				if element.Value != "" {
					t.Errorf("non-field %s exposed a classifier value: %q", element.Role, element.Value)
				}
			}

			questions, _ := buildActQuestions(space, "Search for widgets and agree to terms")
			click := questions["click_target"].Criteria.(map[string]any)
			for _, index := range []string{"1", "2", "3"} {
				criterion := click[index].(map[string]any)
				if value, present := criterion["current_value"]; present {
					t.Errorf("non-field %s supplied current_value=%q", index, value)
				}
			}
			if got := click["1"].(map[string]any)["checked"]; got != checked {
				t.Errorf("checkbox criterion checked=%v, want %s", got, checked)
			}
			for _, tc := range []struct{ head, index, value string }{
				{"click_target", "4", ""},
				{"type_text_target", "4", ""},
				{"type_text_target", "5", "widgets"},
				{"select_target", "6:1", "Relevance"},
			} {
				criterion := questions[tc.head].Criteria.(map[string]any)[tc.index].(map[string]any)
				if value, present := criterion["current_value"]; !present || value != tc.value {
					t.Errorf("%s[%s] current_value=%v (present=%v), want %q", tc.head, tc.index, value, present, tc.value)
				}
			}
		})
	}
}
