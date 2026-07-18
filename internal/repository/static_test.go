package repository

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadRejectsUnsafeSizeBoundsBeforeOpeningFile(t *testing.T) {
	for _, maxBytes := range []int64{-1, 0, math.MaxInt64} {
		if _, err := Load(filepath.Join(t.TempDir(), "missing.json"), maxBytes); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Load(_, %d) error = %v", maxBytes, err)
		}
	}
}

func TestLoadRejectsDuplicateRepositoryIdentifiers(t *testing.T) {
	for _, data := range []string{
		`[{"id":1,"zoekt_id":7,"name":"acme/one"},{"id":1,"zoekt_id":8,"name":"acme/two"}]`,
		`[{"id":1,"zoekt_id":7,"name":"acme/one"},{"id":2,"zoekt_id":7,"name":"acme/two"}]`,
		`[{"id":1,"zoekt_id":7,"name":"acme/one"},{"id":2,"zoekt_id":8,"name":"acme/one"}]`,
	} {
		path := filepath.Join(t.TempDir(), "repositories.json")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path, 1024); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Load() error = %v", err)
		}
	}
}

func TestStaticReturnsDefensiveRepositoryCopies(t *testing.T) {
	registry, err := NewStatic([]Repository{{ID: 1, ZoektID: 7, Name: "acme/one"}})
	if err != nil {
		t.Fatal(err)
	}
	got := registry.Repositories()
	got[0].Name = "changed"
	if again := registry.Repositories(); again[0].Name != "acme/one" {
		t.Fatalf("Repositories() = %#v", again)
	}
}

func TestStaticDeepCopiesLastIndexedAt(t *testing.T) {
	indexedAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	repositories := []Repository{{ID: 1, ZoektID: 7, Name: "acme/one", LastIndexedAt: &indexedAt}}
	registry, err := NewStatic(repositories)
	if err != nil {
		t.Fatal(err)
	}
	indexedAt = indexedAt.Add(time.Hour)
	if got := registry.Repositories()[0].LastIndexedAt; !got.Equal(time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("original timestamp mutation leaked: %s", got)
	}
	got := registry.Repositories()
	*got[0].LastIndexedAt = got[0].LastIndexedAt.Add(time.Hour)
	if again := registry.Repositories()[0].LastIndexedAt; !again.Equal(time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("returned timestamp mutation leaked: %s", again)
	}
}

func TestNewStaticRejectsDuplicateRepositoryIdentifiers(t *testing.T) {
	_, err := NewStatic([]Repository{{ID: 1, ZoektID: 7, Name: "acme/one"}, {ID: 1, ZoektID: 8, Name: "acme/two"}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewStatic() error = %v", err)
	}
}
