//go:build !darwin && !linux

package tools

import "os"

func openClassifyFile(path string) (*os.File, error) {
	return os.Open(path)
}
