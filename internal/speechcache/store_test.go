package speechcache

import (
	"bytes"
	"testing"
	"time"
)

func TestPersistentStoreLoadsClipAfterRestart(t *testing.T) {
	dir := t.TempDir()
	wantPayload := []byte("fake audio payload")

	first := NewPersistent(time.Hour, dir)
	clipID := first.Save("audio/wav", wantPayload)
	if clipID == "" {
		t.Fatal("expected clip id")
	}

	second := NewPersistent(time.Hour, dir)
	contentType, gotPayload, ok := second.Load(clipID)
	if !ok {
		t.Fatal("expected persisted clip to load from a new store")
	}
	if contentType != "audio/wav" {
		t.Fatalf("expected content type audio/wav, got %q", contentType)
	}
	if !bytes.Equal(gotPayload, wantPayload) {
		t.Fatalf("payload mismatch: got %q want %q", gotPayload, wantPayload)
	}
}

func TestPersistentStoreFallsBackToDiskAfterMemoryTTL(t *testing.T) {
	dir := t.TempDir()
	store := NewPersistent(time.Nanosecond, dir)
	clipID := store.Save("audio/mpeg", []byte("payload"))
	if clipID == "" {
		t.Fatal("expected clip id")
	}

	time.Sleep(time.Millisecond)
	contentType, payload, ok := store.Load(clipID)
	if !ok {
		t.Fatal("expected disk fallback after in-memory TTL cleanup")
	}
	if contentType != "audio/mpeg" {
		t.Fatalf("expected content type audio/mpeg, got %q", contentType)
	}
	if string(payload) != "payload" {
		t.Fatalf("expected payload, got %q", payload)
	}
}

func TestPersistentStoreRejectsUnsafeClipID(t *testing.T) {
	store := NewPersistent(time.Hour, t.TempDir())
	if _, _, ok := store.Load("../secret"); ok {
		t.Fatal("expected unsafe path-like clip id to be rejected")
	}
}
