//go:build darwin && !cgo

package tools

import "fmt"

func listCameraDevicesDarwin() ([]CameraDevice, error) {
	return nil, fmt.Errorf("camera device listing on darwin requires a cgo-enabled build")
}
