//go:build windows

package tools

import "context"

func listCameraDevicesWindows(ctx context.Context) ([]CameraDevice, error) {
	names, err := listCamerasWindowsFFmpeg(ctx)
	if err != nil {
		return nil, err
	}
	devices := make([]CameraDevice, 0, len(names))
	for i, name := range names {
		devices = append(devices, CameraDevice{
			Index: i + 1,
			Name:  name,
		})
	}
	return devices, nil
}
