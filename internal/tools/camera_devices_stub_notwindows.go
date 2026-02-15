//go:build !windows

package tools

import (
	"context"
	"fmt"
)

func listCameraDevicesWindows(ctx context.Context) ([]CameraDevice, error) {
	_ = ctx
	return nil, fmt.Errorf("camera device listing is not supported on this platform")
}
