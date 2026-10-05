//go:build !darwin && !linux

package tools

import (
	"fmt"
	"os"
)

func openRelevanceFile(path string) (*os.File, error) {
	// Do not send content on platforms without a race-safe NOFOLLOW traversal.
	return nil, fmt.Errorf("secure relevance file opening is unsupported on this platform; use read for %s", path)
}
