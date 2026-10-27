package storage

import (
	"context"
	"errors"
	"testing"
)

func TestLocalStore(t *testing.T) {
	s, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := ScreenshotKey("job-1", 7)
	if err := s.Put(ctx, key, []byte("png"), "image/png"); err != nil {
		t.Fatal(err)
	}
	b, err := s.Get(ctx, key)
	if err != nil || string(b) != "png" {
		t.Fatalf("get: %q %v", b, err)
	}
	if err := s.DeletePrefix(ctx, JobPrefix("job-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	for _, bad := range []string{"", "/abs", "a/../b", `a\b`} {
		if err := s.Put(ctx, bad, nil, ""); err == nil {
			t.Errorf("key %q accepted", bad)
		}
	}
	if err := s.DeletePrefix(ctx, "jobs"); err == nil {
		t.Error("prefix without trailing slash accepted")
	}
}
