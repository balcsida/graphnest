package repository

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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

func TestNewStaticRejectsDuplicateRepositoryIdentifiers(t *testing.T) {
	_, err := NewStatic([]Repository{{ID: 1, ZoektID: 7, Name: "acme/one"}, {ID: 1, ZoektID: 8, Name: "acme/two"}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewStatic() error = %v", err)
	}
}
