package main
import (
	"fmt"
	"github.com/bmatcuk/doublestar/v4"
)
func main() {
	matched, _ := doublestar.PathMatch("**/*.go", "internal/tools/find_files.go")
	fmt.Println("**/*.go:", matched)
	matched, _ = doublestar.PathMatch("*.go", "internal/tools/find_files.go")
	fmt.Println("*.go:", matched)
	matched, _ = doublestar.PathMatch("internal/**/*.go", "internal/tools/find_files.go")
	fmt.Println("internal/**/*.go:", matched)
	// what about original behavior of `doublestar.FilepathGlob("path/to/*.go")`? It matches `path/to/file.go`.
	// Our `PathMatch("*.go", "path/to/file.go")` will be false. 
	// If a user enters `pattern: "*.go"`, does find_files match only in root, or recursively?
	// The original code uses doublestar.FilepathGlob(filepath.Join(basePath, pattern)).
	// If pattern is "*.go", it matches only "*.go" in basePath!
	// Wait, is that true? Let's check!
}
