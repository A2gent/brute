//go:build !linux

package tools

import "fmt"

func listCameraDevicesLinux() ([]CameraDevice, error) {
	return nil, fmt.Errorf("camera device listing is not supported on this platform")
}
