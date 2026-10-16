package tools

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

const maxReadImageBytes = 20 * 1024 * 1024

// Return images through the existing admission channel, not as binary text.
// This lets the model explicitly reopen an aged-out screenshot with read.
func readImage(file *os.File, size int64) (*Result, error) {
	header := make([]byte, 512)
	n, err := file.ReadAt(header, 0)
	if err != nil && err != io.EOF {
		return nil, err
	}
	mediaType := http.DetectContentType(header[:n])
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return nil, nil
	}
	if size > maxReadImageBytes {
		return &Result{Success: false, Error: fmt.Sprintf("image exceeds %d byte limit", maxReadImageBytes)}, nil
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxReadImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxReadImageBytes {
		return &Result{Success: false, Error: fmt.Sprintf("image exceeds %d byte limit", maxReadImageBytes)}, nil
	}
	inline := map[string]interface{}{"media_type": mediaType, "data_base64": base64.StdEncoding.EncodeToString(raw)}
	return &Result{
		Success: true,
		Output:  fmt.Sprintf("Image: %s (%s, %d bytes)", filepath.Base(file.Name()), mediaType, len(raw)),
		Metadata: map[string]interface{}{
			"image_inline":     inline,
			"admission_images": []interface{}{inline},
			"image_file":       map[string]interface{}{"path": file.Name()},
		},
	}, nil
}
