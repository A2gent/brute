//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func listCameraDevicesLinux() ([]CameraDevice, error) {
	paths, err := filepath.Glob("/dev/video*")
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	devices := make([]CameraDevice, 0, len(paths))
	for _, path := range paths {
		base := filepath.Base(path) // video0
		if !strings.HasPrefix(base, "video") {
			continue
		}
		numRaw := strings.TrimPrefix(base, "video")
		num, convErr := strconv.Atoi(numRaw)
		if convErr != nil {
			continue
		}
		name := strings.TrimSpace(readFirstLine(filepath.Join("/sys/class/video4linux", base, "name")))
		if name == "" {
			name = path
		}
		devices = append(devices, CameraDevice{
			Index: num + 1,
			Name:  name,
			ID:    path,
		})
	}
	return devices, nil
}

func readFirstLine(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line := string(raw)
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	return strings.TrimSpace(line)
}
