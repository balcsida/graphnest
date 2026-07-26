package authn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestSessionManagerStoresOpaqueTokenAndPrincipal(t *testing.T) {
	store := &recordingSessionStore{}
	manager := SessionManager{Store: store, TTL: time.Hour, Now: func() time.Time { return time.Unix(100, 0) }, Rand: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32))}
	token, expiresAt, err := manager.Create(t.Context(), testIdentity(), testPrincipal())
	if err != nil {
		t.Fatal(err)
	}
	if token != "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI" || expiresAt != time.Unix(3700, 0) {
		t.Fatalf("token=%q expiresAt=%s", token, expiresAt)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token decode = %x, %v", raw, err)
	}
	if want := sha256.Sum256(raw); store.created.TokenHash != want || store.created.DisplayName != "Ada" || store.created.Principal.Subject != "opaque-subject" || store.created.Principal.DisplayName != "" {
		t.Fatalf("stored = %#v", store.created)
	}
}

func TestSessionManagerAuthenticatesStoredScopeWithoutAliasing(t *testing.T) {
	store := &recordingSessionStore{session: SessionRecord{Principal: Principal{Subject: "opaque-subject", Method: "oidc", InstallationID: 10, RepositoryIDs: []int64{101, 102}}, DisplayName: "Ada"}}
	manager := SessionManager{Store: store, Now: func() time.Time { return time.Unix(100, 0) }}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	got, err := manager.Authenticate(t.Context(), token)
	if err != nil || got.DisplayName != "Ada" || !reflect.DeepEqual(got.RepositoryIDs, []int64{101, 102}) {
		t.Fatalf("principal=%#v err=%v", got, err)
	}
	got.RepositoryIDs[0] = 999
	if store.session.Principal.RepositoryIDs[0] != 101 {
		t.Fatal("returned repositories alias store state")
	}
}

func TestSessionManagerRejectsUnauthenticatedTokens(t *testing.T) {
	manager := SessionManager{Store: &recordingSessionStore{sessionErr: errors.New("missing")}, Now: time.Now}
	for _, token := range []string{"!", base64.RawURLEncoding.EncodeToString([]byte{1}), base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))} {
		if _, err := manager.Authenticate(t.Context(), token); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("token %q error = %v", token, err)
		}
	}
}

func TestSessionManagerRejectsStoredAdministratorAndRevokesIdempotently(t *testing.T) {
	store := &recordingSessionStore{session: SessionRecord{Principal: Principal{Administrator: true}}}
	manager := SessionManager{Store: store, Now: time.Now}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	if _, err := manager.Authenticate(t.Context(), token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("error = %v", err)
	}
	if err := manager.Revoke(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	if store.deleted != sha256.Sum256(bytes.Repeat([]byte{1}, 32)) {
		t.Fatalf("deleted = %x", store.deleted)
	}
}

func TestSessionManagerDoesNotPersistAdministrator(t *testing.T) {
	store := &recordingSessionStore{}
	manager := SessionManager{Store: store, TTL: time.Hour, Rand: bytes.NewReader(bytes.Repeat([]byte{1}, 32))}
	if _, _, err := manager.Create(t.Context(), testIdentity(), Principal{Subject: "admin", Method: "oidc", Administrator: true}); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(store.created, SessionRecord{}) {
		t.Fatalf("stored = %#v", store.created)
	}
}

func testIdentity() Identity {
	return Identity{Provider: "oidc", Issuer: "issuer", Subject: "subject", DisplayName: "Ada", Groups: []string{"engineering"}}
}
func testPrincipal() Principal {
	return Principal{Subject: "opaque-subject", Method: "oidc", InstallationID: 10, RepositoryIDs: []int64{101, 102}}
}

type recordingSessionStore struct {
	created    SessionRecord
	session    SessionRecord
	sessionErr error
	deleted    [32]byte
}

func (s *recordingSessionStore) CreateLoginFlow(context.Context, LoginFlow) error { return nil }
func (s *recordingSessionStore) ConsumeLoginFlow(context.Context, [32]byte, [32]byte, string, time.Time) (LoginFlow, error) {
	return LoginFlow{}, nil
}
func (s *recordingSessionStore) CreateSession(_ context.Context, session SessionRecord) error {
	s.created = session
	return nil
}
func (s *recordingSessionStore) Session(_ context.Context, _ [32]byte, _ time.Time) (SessionRecord, error) {
	return s.session, s.sessionErr
}
func (s *recordingSessionStore) DeleteSession(_ context.Context, token [32]byte) error {
	s.deleted = token
	return nil
}
func (s *recordingSessionStore) DeleteExpiredAuth(context.Context, time.Time) (int64, int64, error) {
	return 0, 0, nil
}
