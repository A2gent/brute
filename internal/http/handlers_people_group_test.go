package http

import "testing"

func TestParseProjectPersonFrontmatterGroupsOverrideFolderGroup(t *testing.T) {
	content := `---
type: person
name: Related Contact
groups:
  - Family
importance: 7
---
`

	person, ok := parseProjectPerson("08-Люди/6 - знакомые/Related Contact.md", "08-Люди", content)
	if !ok {
		t.Fatal("expected frontmatter person to be parsed")
	}
	if len(person.Groups) != 1 || person.Groups[0] != "Family" {
		t.Fatalf("groups = %v, want frontmatter override [Family]", person.Groups)
	}
}
