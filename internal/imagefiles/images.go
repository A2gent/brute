// Package imagefiles stores immutable image attachments outside SQLite.
package imagefiles

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"
)

type Attachment struct {
	Name       string `json:"name,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
	URL        string `json:"url,omitempty"`
	Path       string `json:"path,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Root       string `json:"-"`
}

// Save writes and syncs the original before returning a reference safe to persist.
// Failed database writes may leave harmless content-addressed files, never dangling references.
func (a Attachment) Save(root, sessionID string) (Attachment, error) {
	a.Root = root
	data := strings.TrimSpace(a.DataBase64)
	if data == "" && strings.HasPrefix(a.URL, "data:image/") {
		header, payload, ok := strings.Cut(a.URL, ";base64,")
		if ok {
			data = payload
			a.MediaType = strings.TrimPrefix(header, "data:")
			a.URL = ""
		}
	}
	if data == "" {
		return a, nil
	}
	if !safeID(sessionID) {
		return a, fmt.Errorf("invalid image session ID")
	}
	raw, err := DecodeBase64(data)
	if err != nil || len(raw) == 0 {
		return a, fmt.Errorf("invalid image base64: %v", err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return a, fmt.Errorf("decode image: %w", err)
	}
	ext := map[string]string{"jpeg": "jpg", "png": "png", "gif": "gif", "webp": "webp"}[format]
	if ext == "" {
		return a, fmt.Errorf("unsupported image format %q", format)
	}
	sum := sha256.Sum256(raw)
	a.SHA256 = hex.EncodeToString(sum[:])
	a.Path = filepath.ToSlash(filepath.Join("sessions", sessionID, "images", a.SHA256+"."+ext))
	a.MediaType = "image/" + format
	a.Width, a.Height = cfg.Width, cfg.Height
	// OpenRoot prevents traversal through symlinks as well as ../ components.
	dir, err := os.OpenRoot(root)
	if err != nil {
		return a, err
	}
	defer dir.Close()
	if err := dir.MkdirAll(filepath.Dir(a.Path), 0700); err != nil {
		return a, err
	}
	existing, err := dir.ReadFile(a.Path)
	if err == nil {
		if sha256.Sum256(existing) != sum {
			return a, fmt.Errorf("existing image is corrupt: %s", a.Path)
		}
	} else if !os.IsNotExist(err) {
		return a, err
	} else {
		// Publish atomically: parallel saves must never observe a partial image.
		temporary := a.Path + "." + rand.Text() + ".tmp"
		defer dir.Remove(temporary)
		f, err := dir.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return a, err
		}
		_, err = f.Write(raw)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return a, err
		}
		if err := dir.Rename(temporary, a.Path); err != nil {
			return a, err
		}
	}
	// Sync every newly reachable directory entry before SQLite can commit a reference.
	for _, path := range []string{filepath.Dir(a.Path), filepath.Join("sessions", sessionID), "sessions", "."} {
		directory, err := dir.Open(path)
		if err != nil {
			return a, err
		}
		err = directory.Sync()
		closeErr := directory.Close()
		if err != nil {
			return a, err
		}
		if closeErr != nil {
			return a, closeErr
		}
	}
	a.DataBase64 = ""
	return a, nil
}

func safeID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Read validates the reference and checksum. It is only called at the image boundary.
func (a Attachment) Read() ([]byte, error) {
	if a.Path == "" {
		return DecodeBase64(a.DataBase64)
	}
	if !ValidPath(a.Path) || a.Root == "" {
		return nil, fmt.Errorf("invalid image reference %q", a.Path)
	}
	dir, err := os.OpenRoot(a.Root)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	raw, err := dir.ReadFile(a.Path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != a.SHA256 {
		return nil, fmt.Errorf("image checksum mismatch: %s", a.Path)
	}
	return raw, nil
}

func ValidPath(path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[0] != "sessions" || !safeID(parts[1]) || parts[2] != "images" {
		return false
	}
	hash, ext, ok := strings.Cut(parts[3], ".")
	if !ok || len(hash) != 64 || !(ext == "png" || ext == "jpg" || ext == "gif" || ext == "webp") {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func (a Attachment) Inline() (Attachment, error) {
	if a.Path == "" {
		return a, nil
	}
	raw, err := a.Read()
	if err != nil {
		return a, err
	}
	a.DataBase64 = base64.StdEncoding.EncodeToString(raw)
	a.URL = ""
	return a, nil
}

// DecodeBase64 matches the padded and unpadded encodings accepted at ingress.
func DecodeBase64(data string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return base64.RawStdEncoding.DecodeString(data)
	}
	return raw, nil
}
