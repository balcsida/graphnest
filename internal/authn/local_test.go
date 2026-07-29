package authn

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type localStoreStub struct {
	mu         sync.Mutex
	userID     int64
	credential PasswordCredential
	lookupErr  error
	consumeErr error
	clearErr   error
	blocked    map[[32]byte]time.Time
	consumed   [][32]byte
	cleared    [][32]byte
}

func (s *localStoreStub) PasswordCredential(_ context.Context, _ string) (int64, PasswordCredential, error) {
	return s.userID, s.credential, s.lookupErr
}

func (s *localStoreStub) ConsumeLoginAttempt(_ context.Context, key [32]byte, _ time.Time) (bool, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumed = append(s.consumed, key)
	if s.consumeErr != nil {
		return false, time.Time{}, s.consumeErr
	}
	retryAt, blocked := s.blocked[key]
	return !blocked, retryAt, nil
}

func (s *localStoreStub) ClearLoginFailures(_ context.Context, key [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleared = append(s.cleared, key)
	return s.clearErr
}

func localCredential(t *testing.T, forceRotation bool) PasswordCredential {
	t.Helper()
	credential, err := HashPassword([]byte("sixteen-byte-secret"), bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	credential.ForceRotation = forceRotation
	return credential
}

func localAuthenticator(t *testing.T, store *localStoreStub) LocalAuthenticator {
	t.Helper()
	sessionStore := &sessionStoreStub{}
	return LocalAuthenticator{
		Store: store,
		Sessions: &SessionManager{
			Store: sessionStore, IdleTTL: time.Minute, TTL: time.Hour,
			Now:  func() time.Time { return time.Unix(100, 0) },
			Rand: bytes.NewReader(bytes.Repeat([]byte{9}, 32)),
		},
		Now:   func() time.Time { return time.Unix(100, 0) },
		Dummy: localCredential(t, false),
	}
}

func TestLocalAuthenticateConsumesAccountAndCanonicalSourceThenCreatesSession(t *testing.T) {
	store := &localStoreStub{userID: 42, credential: localCredential(t, true)}
	authenticator := localAuthenticator(t, store)
	result, err := authenticator.Authenticate(t.Context(), "Recovery-Admin", []byte("sixteen-byte-secret"), "[2001:0db8::1]:443")
	if err != nil {
		t.Fatal(err)
	}
	if result.Token == "" || !result.ForceRotation {
		t.Fatalf("result = %#v", result)
	}
	accountKey, sourceKey := localThrottleKeys("recovery-admin", "2001:db8::1")
	if len(store.consumed) != 2 || store.consumed[0] != accountKey || store.consumed[1] != sourceKey {
		t.Fatalf("consumed = %#v", store.consumed)
	}
	if len(store.cleared) != 2 || store.cleared[0] != accountKey || store.cleared[1] != sourceKey {
		t.Fatalf("cleared = %#v", store.cleared)
	}
}

func TestLocalAuthenticateConsumesBothKeysWhenOneIsBlocked(t *testing.T) {
	accountKey, sourceKey := localThrottleKeys("recovery-admin", "192.0.2.1")
	for _, blockedKey := range [][32]byte{accountKey, sourceKey} {
		store := &localStoreStub{userID: 42, credential: localCredential(t, false), blocked: map[[32]byte]time.Time{blockedKey: time.Unix(160, 0)}}
		authenticator := localAuthenticator(t, store)
		_, err := authenticator.Authenticate(t.Context(), "recovery-admin", []byte("wrong-password"), "192.0.2.1:1234")
		var throttled *LoginThrottleError
		if !errors.As(err, &throttled) || throttled.RetryAfter != time.Minute || len(store.consumed) != 2 {
			t.Fatalf("error=%v consumed=%d", err, len(store.consumed))
		}
	}
}

func TestLocalAuthenticateUsesDummyCredentialAndGenericErrors(t *testing.T) {
	store := &localStoreStub{lookupErr: errors.New("missing")}
	authenticator := localAuthenticator(t, store)
	verified := 0
	authenticator.verify = func(password []byte, credential PasswordCredential) bool {
		verified++
		return VerifyPassword(password, credential)
	}
	_, err := authenticator.Authenticate(t.Context(), "missing-admin", []byte("wrong-password"), "192.0.2.1")
	if !errors.Is(err, ErrUnauthenticated) || verified != 1 {
		t.Fatalf("error=%v verifications=%d", err, verified)
	}
}

func TestLocalAuthenticateUsesAtomicConsumeContractConcurrently(t *testing.T) {
	store := &localStoreStub{lookupErr: errors.New("missing")}
	authenticator := localAuthenticator(t, store)
	authenticator.verify = func(password []byte, _ PasswordCredential) bool {
		clear(password)
		return false
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _ = authenticator.Authenticate(t.Context(), "recovery-admin", []byte("wrong-password"), "192.0.2.1")
		}()
	}
	wait.Wait()
	if len(store.consumed) != 16 {
		t.Fatalf("consumed = %d", len(store.consumed))
	}
}

func TestLocalAuthenticateReturnsGenericErrorForWrongOrIneligibleAccount(t *testing.T) {
	for _, test := range []struct {
		name      string
		lookupErr error
		password  string
	}{
		{name: "wrong password", password: "wrong-password"},
		{name: "inactive source or role", lookupErr: errors.New("ineligible"), password: "sixteen-byte-secret"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &localStoreStub{userID: 42, credential: localCredential(t, false), lookupErr: test.lookupErr}
			_, err := localAuthenticator(t, store).Authenticate(t.Context(), "recovery-admin", []byte(test.password), "192.0.2.1")
			if !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLocalAuthenticateFailsClosedOnSessionError(t *testing.T) {
	store := &localStoreStub{userID: 42, credential: localCredential(t, false)}
	authenticator := localAuthenticator(t, store)
	authenticator.Sessions.Store = nil
	if _, err := authenticator.Authenticate(t.Context(), "recovery-admin", []byte("sixteen-byte-secret"), "192.0.2.1"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("error = %v", err)
	}
}

func TestLocalAuthenticateFailsClosedOnThrottleStoreErrors(t *testing.T) {
	for _, store := range []*localStoreStub{
		{userID: 42, credential: localCredential(t, false), consumeErr: errors.New("consume failed")},
		{userID: 42, credential: localCredential(t, false), clearErr: errors.New("clear failed")},
	} {
		_, err := localAuthenticator(t, store).Authenticate(t.Context(), "recovery-admin", []byte("sixteen-byte-secret"), "192.0.2.1")
		if !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestLocalAuthenticatorRequiresValidStartupDummy(t *testing.T) {
	if _, err := NewLocalAuthenticator(&localStoreStub{}, &SessionManager{}, bytes.NewReader(make([]byte, 48))); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalAuthenticator(&localStoreStub{}, &SessionManager{}, bytes.NewReader(nil)); err == nil {
		t.Fatal("short random source accepted")
	}
}
