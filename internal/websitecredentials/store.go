// Package websitecredentials stores website credentials separately from settings.
// All record fields are encrypted; only explicit Get callers receive passwords.
package websitecredentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrStorage  = errors.New("website credential storage unavailable")
	ErrNotFound = errors.New("website credential not found")
	ErrInvalid  = errors.New("invalid website credentials: require an http/https URL with a host and no embedded credentials, a login, and a password on create")
)

const header = "A2WC1\n"

type Metadata struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Login       string    `json:"login"`
	HasPassword bool      `json:"has_password"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Credential struct {
	Metadata
	Password string `json:"password"`
}

type Store struct{ dir string }

// New is lazy: listing a fresh installation does not create files.
func New(dataDir string) *Store {
	if strings.TrimSpace(dataDir) == "" {
		return &Store{}
	}
	return &Store{dir: filepath.Join(dataDir, "website-credentials")}
}

func validate(site, login, password string, create bool) error {
	u, err := url.Parse(site)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.TrimSpace(login) == "" || (create && password == "") {
		return ErrInvalid
	}
	return nil
}

func (s *Store) List() ([]Metadata, error) {
	out := []Metadata{}
	err := s.access(false, func(records *[]Credential, _ []byte) error {
		for _, record := range *records {
			out = append(out, record.Metadata)
		}
		return nil
	})
	return out, err
}

func (s *Store) Get(id string) (Credential, error) {
	var out Credential
	err := s.access(false, func(records *[]Credential, _ []byte) error {
		for _, record := range *records {
			if record.ID == id {
				out = record
				return nil
			}
		}
		return ErrNotFound
	})
	if err == nil && out.ID == "" {
		err = ErrNotFound
	}
	return out, err
}

func (s *Store) Create(site, login, password string) (Metadata, error) {
	if err := validate(site, login, password, true); err != nil {
		return Metadata{}, err
	}
	var out Metadata
	err := s.access(true, func(records *[]Credential, key []byte) error {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return ErrStorage
		}
		now := time.Now().UTC()
		out = Metadata{hex.EncodeToString(id), site, login, true, now, now}
		*records = append(*records, Credential{out, password})
		return s.save(*records, key)
	})
	return out, err
}

// Update treats omitted and empty passwords identically, preserving the secret.
func (s *Store) Update(id, site, login, password string) (Metadata, error) {
	if err := validate(site, login, password, false); err != nil {
		return Metadata{}, err
	}
	var out Metadata
	err := s.access(false, func(records *[]Credential, key []byte) error {
		for i := range *records {
			record := &(*records)[i]
			if record.ID != id {
				continue
			}
			record.URL, record.Login = site, login
			record.UpdatedAt = time.Now().UTC()
			if password != "" {
				record.Password = password
			}
			out = record.Metadata
			return s.save(*records, key)
		}
		return ErrNotFound
	})
	if err == nil && out.ID == "" {
		err = ErrNotFound
	}
	return out, err
}

func (s *Store) Delete(id string) error {
	found := false
	err := s.access(false, func(records *[]Credential, key []byte) error {
		for i, record := range *records {
			if record.ID == id {
				found = true
				*records = append((*records)[:i], (*records)[i+1:]...)
				return s.save(*records, key)
			}
		}
		return ErrNotFound
	})
	if err == nil && !found {
		err = ErrNotFound
	}
	return err
}

// access holds an OS lock across the full read-modify-rename transaction, so
// separate server processes sharing a data directory cannot lose updates.
func (s *Store) access(create bool, fn func(*[]Credential, []byte) error) error {
	if s == nil || s.dir == "" {
		return ErrStorage
	}
	info, err := os.Lstat(s.dir)
	if errors.Is(err, os.ErrNotExist) && !create {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrStorage
	}
	if info != nil && !info.IsDir() {
		return ErrStorage
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return ErrStorage
	}
	// A new vault entry must be durable in its parent before the key is written
	// into it, otherwise a crash can drop the key while records later survive.
	if info == nil && syncDir(filepath.Dir(s.dir)) != nil {
		return ErrStorage
	}
	if err := os.Chmod(s.dir, 0700); err != nil {
		return ErrStorage
	}
	lockPath := filepath.Join(s.dir, ".lock")
	if !regularOrMissing(lockPath) {
		return ErrStorage
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return ErrStorage
	}
	defer lock.Close()
	if err := lockFile(lock); err != nil {
		return ErrStorage
	}
	defer unlockFile(lock)

	recordsPath := filepath.Join(s.dir, "records.enc")
	keyPath := filepath.Join(s.dir, "key")
	if !regularOrMissing(recordsPath) || !regularOrMissing(keyPath) {
		return ErrStorage
	}
	data, dataErr := os.ReadFile(recordsPath)
	if dataErr != nil && !errors.Is(dataErr, os.ErrNotExist) {
		return ErrStorage
	}
	key, keyErr := os.ReadFile(keyPath)
	if keyErr != nil {
		// Never replace a missing key if any records file exists, even if empty
		// or corrupt. Recovering that key from backup is the only safe repair.
		if !errors.Is(keyErr, os.ErrNotExist) || dataErr == nil {
			return ErrStorage
		}
		if !create {
			records := []Credential{}
			return fn(&records, nil)
		}
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return ErrStorage
		}
		if err := atomicWrite(keyPath, key); err != nil {
			return ErrStorage
		}
	} else if len(key) != 32 {
		return ErrStorage
	}
	if err := os.Chmod(keyPath, 0600); err != nil {
		return ErrStorage
	}
	records := []Credential{}
	if dataErr == nil {
		if err := os.Chmod(recordsPath, 0600); err != nil {
			return ErrStorage
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return ErrStorage
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil || len(data) < len(header)+gcm.NonceSize()+gcm.Overhead() || string(data[:len(header)]) != header {
			return ErrStorage
		}
		nonce := data[len(header) : len(header)+gcm.NonceSize()]
		plain, err := gcm.Open(nil, nonce, data[len(header)+gcm.NonceSize():], []byte(header))
		if err != nil || json.Unmarshal(plain, &records) != nil || records == nil {
			return ErrStorage
		}
	}
	return fn(&records, key)
}

func regularOrMissing(path string) bool {
	info, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist) || (err == nil && info.Mode().IsRegular())
}

func (s *Store) save(records []Credential, key []byte) error {
	plain, err := json.Marshal(records)
	if err != nil {
		return ErrStorage
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return ErrStorage
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ErrStorage
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return ErrStorage
	}
	data := append([]byte(header), nonce...)
	data = gcm.Seal(data, nonce, plain, []byte(header))
	return atomicWrite(filepath.Join(s.dir, "records.enc"), data)
}

// syncDir is replaceable so tests can observe ordering and inject failures.
var syncDir = syncDirectory

// Atomic replacement never exposes partial JSON or ciphertext. Temp files
// contain only encrypted records or a key, never plaintext credentials. The
// directory sync after rename orders a new key's durability before any
// records.enc that depends on it.
func atomicWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return ErrStorage
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return ErrStorage
	}
	if err := file.Sync(); err != nil {
		return ErrStorage
	}
	if err := file.Close(); err != nil {
		return ErrStorage
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return ErrStorage
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return ErrStorage
	}
	return nil
}
