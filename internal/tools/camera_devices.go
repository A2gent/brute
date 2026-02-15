package tools

import (
	"context"
	"fmt"
	"runtime"
)

type CameraDevice struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	ID    string `json:"id,omitempty"`
}

func ListCameraDevices(ctx context.Context) ([]CameraDevice, error) {
	switch runtime.GOOS {
	case "darwin":
		return listCameraDevicesDarwin()
	case "linux":
		return listCameraDevicesLinux()
	case "windows":
		return listCameraDevicesWindows(ctx)
	default:
		return nil, fmt.Errorf("camera device listing is not supported on %s", runtime.GOOS)
	}
}
