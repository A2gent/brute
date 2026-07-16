package filesearch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeQueryHandlesQuotedRelativePaths(t *testing.T) {
	input := "  `./src\\components/SearchBox.tsx`  "
	if got := NormalizeQuery(input); got != "src/components/SearchBox.tsx" {
		t.Fatalf("NormalizeQuery() = %q", got)
	}
}

func TestIndexSearchesPathsAndSkipsDependencyFolders(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "src/components/SearchBox.tsx", "export function SearchBox() { return null }\n")
	writeTestFile(t, root, "node_modules/pkg/SearchBox.tsx", "export const dependency = true\n")
	writeTestFile(t, root, "vendor/pkg/search_box.go", "package vendor\n")

	idx, err := Build(context.Background(), root, Options{MaxContentBytes: 4 << 20, MaxFileBytes: 512 << 10})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	result := idx.Search(SearchRequest{Query: "searchbox", FileLimit: 10, IncludeContent: false})
	if len(result.FileMatches) == 0 {
		t.Fatalf("expected src SearchBox match, got none")
	}
	if result.FileMatches[0].Path != "src/components/SearchBox.tsx" {
		t.Fatalf("expected src match first, got %q", result.FileMatches[0].Path)
	}
	for _, match := range result.FileMatches {
		if match.Path == "node_modules/pkg/SearchBox.tsx" || match.Path == "vendor/pkg/search_box.go" {
			t.Fatalf("dependency folder path should not be indexed: %+v", match)
		}
	}
}

func TestIndexSearchesContentWithLinePreview(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "spec/models/query_spec.rb", "RSpec.describe Spareto::Search::Query do\n  it 'works'\nend\n")
	writeTestFile(t, root, "src/other.rb", "class Other\nend\n")

	idx, err := Build(context.Background(), root, Options{MaxContentBytes: 4 << 20, MaxFileBytes: 512 << 10})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	result := idx.Search(SearchRequest{Query: "spareto::search::query", FileLimit: 5, ContentLimit: 5, IncludeContent: true})
	if len(result.ContentMatches) == 0 {
		t.Fatalf("expected content match, got none")
	}
	match := result.ContentMatches[0]
	if match.Path != "spec/models/query_spec.rb" || match.Line != 1 {
		t.Fatalf("unexpected content match: %+v", match)
	}
	if match.Preview != "RSpec.describe Spareto::Search::Query do" {
		t.Fatalf("unexpected preview: %q", match.Preview)
	}
}

func TestIndexPathSearchToleratesSmallTypos(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "spec/models/spareto/search/query_spec.rb", "RSpec.describe 'query'\n")

	idx, err := Build(context.Background(), root, Options{MaxContentBytes: 4 << 20, MaxFileBytes: 512 << 10})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	result := idx.Search(SearchRequest{Query: "quey_spec", FileLimit: 5, IncludeContent: false})
	if len(result.FileMatches) == 0 {
		t.Fatalf("expected fuzzy filename match, got none")
	}
	if result.FileMatches[0].Path != "spec/models/spareto/search/query_spec.rb" {
		t.Fatalf("expected query_spec first, got %q", result.FileMatches[0].Path)
	}
}

func TestManagerInvalidateRebuildsIndex(t *testing.T) {
	SetIndexingEnabled(true)
	t.Cleanup(func() { SetIndexingEnabled(false) })

	root := t.TempDir()
	writeTestFile(t, root, "src/old.ts", "export const oldValue = true\n")

	manager := NewManager(ManagerOptions{IndexOptions: Options{MaxContentBytes: 4 << 20, MaxFileBytes: 512 << 10}})
	first, err := manager.Search(context.Background(), root, SearchRequest{Query: "newValue", ContentLimit: 5, IncludeContent: true}, true)
	if err != nil {
		t.Fatalf("initial Search returned error: %v", err)
	}
	if len(first.ContentMatches) != 0 {
		t.Fatalf("did not expect newValue before file exists")
	}

	writeTestFile(t, root, "src/new.ts", "export const newValue = true\n")
	manager.Invalidate(root)
	second, err := manager.Search(context.Background(), root, SearchRequest{Query: "newValue", ContentLimit: 5, IncludeContent: true}, true)
	if err != nil {
		t.Fatalf("second Search returned error: %v", err)
	}
	if len(second.ContentMatches) == 0 || second.ContentMatches[0].Path != "src/new.ts" {
		t.Fatalf("expected rebuilt index to find src/new.ts, got %+v", second.ContentMatches)
	}
}

func TestIndexHonorsMaxIndexBytes(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", strings.Repeat("alpha needle\n", 256))
	writeTestFile(t, root, "b.txt", strings.Repeat("beta needle\n", 256))

	idx, err := Build(context.Background(), root, Options{
		MaxIndexBytes:   6 << 10,
		MaxContentBytes: 4 << 20,
		MaxFileBytes:    512 << 10,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if !idx.Stats().Truncated {
		t.Fatalf("expected index to report truncation when max index bytes is tiny")
	}
	if idx.Stats().IndexedFiles != 2 {
		t.Fatalf("expected paths to remain indexed even when content is truncated, got %d", idx.Stats().IndexedFiles)
	}
	if idx.Stats().IndexedContentFiles != 0 {
		t.Fatalf("expected content indexing to be skipped, got %d content files", idx.Stats().IndexedContentFiles)
	}
}

func writeTestFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("failed to create parent dir: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", relPath, err)
	}
}
