//go:build !darwin

package tools

import "fmt"

func listCameraDevicesDarwin() ([]CameraDevice, error) {
	return nil, fmt.Errorf("camera device listing is not supported on this platform")
}
