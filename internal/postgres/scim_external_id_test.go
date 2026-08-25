//go:build integration

package postgres

import (
	"errors"
	"testing"

	"github.com/balcsida/graphnest/internal/scim"
)

func TestSCIMGroupExternalIDLifecycle(t *testing.T) {
	store := migratedStore(t)
	for _, name := range []string{"No External One", "No External Two"} {
		if _, err := store.CreateGroup(t.Context(), scim.Group{DisplayName: name}); err != nil {
			t.Fatalf("create %q: %v", name, err)
		}
	}
	created, err := store.CreateGroup(t.Context(), scim.Group{ExternalID: "recreated", DisplayName: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGroup(t.Context(), scim.Group{ExternalID: "recreated", DisplayName: "Duplicate"}); !errors.Is(err, scim.ErrUniqueness) {
		t.Fatalf("active duplicate err=%v", err)
	}
	if err := store.DeleteGroup(t.Context(), scimID(t, created.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGroup(t.Context(), scim.Group{ExternalID: "recreated", DisplayName: "Replacement"}); err != nil {
		t.Fatalf("recreate: %v", err)
	}
}
