//go:build darwin || linux

package tools

import (
	"os"
	"syscall"
)

func openClassifyFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
