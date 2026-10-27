package websitecredentials

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStorePersistenceAndLifecycle(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	list, err := s.List()
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("fresh list: %v %v", list, err)
	}
	if _, err := os.Stat(s.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fresh list created files")
	}
	password := "  untouched-secret\n\t "
	created, err := s.Create("https://example.com/login", "alice", password)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"key", "records.enc"} {
		info, err := os.Stat(filepath.Join(s.dir, file))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("file permissions: %v %v", info, err)
		}
	}
	info, _ := os.Stat(s.dir)
	if info.Mode().Perm() != 0700 {
		t.Fatal("directory permissions")
	}
	encrypted, _ := os.ReadFile(filepath.Join(s.dir, "records.enc"))
	for _, plain := range []string{password, "alice", "https://example.com/login"} {
		if bytes.Contains(encrypted, []byte(plain)) {
			t.Fatal("plaintext in record file")
		}
	}
	s = New(root)
	got, err := s.Get(created.ID)
	if err != nil || got.Password != password {
		t.Fatal("password did not persist verbatim", err)
	}
	updated, err := s.Update(created.ID, "http://example.com/new", "bob", "")
	if err != nil || updated.CreatedAt != created.CreatedAt || !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("update: %v %v", updated, err)
	}
	got, _ = s.Get(created.ID)
	if got.Password != password || got.Login != "bob" {
		t.Fatal("empty update replaced password")
	}
	list, _ = s.List()
	encoded, _ := json.Marshal(list)
	if bytes.Contains(encoded, []byte(`"password"`)) || bytes.Contains(encoded, []byte("untouched-secret")) {
		t.Fatal("list leaked password")
	}
	if _, err := s.Update(created.ID, got.URL, got.Login, "replacement"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(created.ID)
	if got.Password != "replacement" {
		t.Fatal("password not rotated")
	}
	if err := s.Delete(created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	list, err = New(root).List()
	if err != nil || len(list) != 0 {
		t.Fatal("delete did not persist", err)
	}
}

func TestStoreValidation(t *testing.T) {
	s := New(t.TempDir())
	for _, site := range []string{"", "example.com", "file:///tmp/a", "https:///path", "https://user:secret@example.com", "https://:443", "https://ex ample.com"} {
		if _, err := s.Create(site, "alice", "password"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted URL %q", site)
		}
	}
	for _, values := range [][2]string{{"", "password"}, {" \n", "password"}, {"alice", ""}} {
		if _, err := s.Create("https://example.com", values[0], values[1]); !errors.Is(err, ErrInvalid) {
			t.Fatal("accepted invalid login/password")
		}
	}
	if _, err := os.Stat(s.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid input wrote files")
	}
}

func TestStoreCorruptionFailsClosed(t *testing.T) {
	for _, broken := range []string{"missing-key", "short-key", "wrong-key", "ciphertext", "empty-records"} {
		t.Run(broken, func(t *testing.T) {
			s := New(t.TempDir())
			record, err := s.Create("https://example.com", "alice", "secret")
			if err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(s.dir, "key")
			recordsPath := filepath.Join(s.dir, "records.enc")
			switch broken {
			case "missing-key":
				err = os.Remove(keyPath)
			case "short-key":
				err = os.WriteFile(keyPath, []byte("broken"), 0600)
			case "wrong-key":
				err = os.WriteFile(keyPath, bytes.Repeat([]byte{1}, 32), 0600)
			case "ciphertext":
				err = os.WriteFile(recordsPath, []byte("corrupt-secret-ciphertext"), 0600)
			case "empty-records":
				err = os.WriteFile(recordsPath, nil, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			beforeKey, _ := os.ReadFile(keyPath)
			beforeRecords, _ := os.ReadFile(recordsPath)
			if _, err := s.List(); !errors.Is(err, ErrStorage) {
				t.Fatal("list accepted corruption", err)
			}
			if _, err := s.Create("https://example.com", "bob", "new-secret"); !errors.Is(err, ErrStorage) {
				t.Fatal("create accepted corruption", err)
			}
			if err := s.Delete(record.ID); !errors.Is(err, ErrStorage) {
				t.Fatal("delete accepted corruption", err)
			}
			afterKey, _ := os.ReadFile(keyPath)
			afterRecords, _ := os.ReadFile(recordsPath)
			if !bytes.Equal(beforeKey, afterKey) || !bytes.Equal(beforeRecords, afterRecords) {
				t.Fatal("corrupt files were overwritten")
			}
			if broken == "missing-key" {
				if _, err := os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("missing key regenerated")
				}
			}
		})
	}
}

func TestStoreConcurrentInstances(t *testing.T) {
	root := t.TempDir()
	const count = 16
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := New(root).Create("https://example.com", "alice", "secret"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	list, err := New(root).List()
	if err != nil || len(list) != count {
		t.Fatalf("lost concurrent update: %d %v", len(list), err)
	}
}

func TestStoreDirectorySyncOrderFailureAndRecovery(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	var synced []string
	fail := ""
	syncDir = func(dir string) error {
		_, keyErr := os.Stat(filepath.Join(s.dir, "key"))
		_, recordsErr := os.Stat(filepath.Join(s.dir, "records.enc"))
		synced = append(synced, fmt.Sprintf("%s key=%t records=%t", dir, keyErr == nil, recordsErr == nil))
		if dir == fail {
			return errors.New("injected sync failure")
		}
		return syncDirectory(dir)
	}
	t.Cleanup(func() { syncDir = syncDirectory })

	// Key rename is durable before records.enc is renamed into place.
	fail = s.dir
	if _, err := s.Create("https://example.com", "alice", "secret"); !errors.Is(err, ErrStorage) {
		t.Fatal("key directory sync failure was ignored", err)
	}
	want := []string{root + " key=false records=false", s.dir + " key=true records=false"}
	if fmt.Sprint(synced) != fmt.Sprint(want) {
		t.Fatalf("sync order: %q", synced)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "records.enc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("records written after failed key sync")
	}

	// Recovery reuses the already-renamed key instead of regenerating it.
	fail, synced = "", nil
	key, _ := os.ReadFile(filepath.Join(s.dir, "key"))
	created, err := s.Create("https://example.com", "alice", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(filepath.Join(s.dir, "key")); !bytes.Equal(key, after) {
		t.Fatal("recovery replaced key")
	}
	if want := []string{s.dir + " key=true records=true"}; fmt.Sprint(synced) != fmt.Sprint(want) {
		t.Fatalf("existing vault sync: %q", synced)
	}

	// A records sync failure is reported; the rename itself is already visible.
	fail = s.dir
	if err := s.Delete(created.ID); !errors.Is(err, ErrStorage) {
		t.Fatal("records directory sync failure was ignored", err)
	}
	fail = ""
	if list, err := New(root).List(); err != nil || len(list) != 0 {
		t.Fatalf("vault unreadable after sync failure: %v %v", list, err)
	}
}
