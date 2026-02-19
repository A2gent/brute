//go:build !darwin

package tools

import "fmt"

func captureCameraPhotoDarwin(cameraIndex int, format string, outputPath string) error {
	return fmt.Errorf("camera capture is not supported on this platform")
}
