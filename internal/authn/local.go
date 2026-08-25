package authn

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/audit"
)

const maxLoginRetryAfter = 15 * time.Minute

type LocalStore interface {
	PasswordCredential(context.Context, string) (int64, PasswordCredential, error)
	ConsumeLoginAttempt(context.Context, [32]byte, time.Time) (bool, time.Time, error)
	ClearLoginFailures(context.Context, [32]byte, [32]byte) error
}

type LocalAuthentication struct {
	UserID        int64
	Token         string
	ExpiresAt     time.Time
	ForceRotation bool
}

type LocalVerification struct {
	UserID        int64
	ForceRotation bool
	Credential    PasswordCredential
	accountKey    [32]byte
	sourceKey     [32]byte
}

func (verification LocalVerification) ThrottleKeys() ([32]byte, [32]byte) {
	return verification.accountKey, verification.sourceKey
}

type LoginThrottleError struct{ RetryAfter time.Duration }

func (e *LoginThrottleError) Error() string { return "login throttled" }

type LocalAuthenticator struct {
	Store    LocalStore
	Sessions *SessionManager
	Now      func() time.Time
	Dummy    PasswordCredential
	verify   func([]byte, PasswordCredential) bool
	Audit    audit.Recorder
}

func NewLocalAuthenticator(store LocalStore, sessions *SessionManager, random io.Reader) (LocalAuthenticator, error) {
	dummyPassword := make([]byte, 32)
	if random == nil {
		random = rand.Reader
	}
	if _, err := io.ReadFull(random, dummyPassword); err != nil {
		return LocalAuthenticator{}, err
	}
	dummy, err := HashPassword(dummyPassword, random)
	if err != nil {
		return LocalAuthenticator{}, err
	}
	return LocalAuthenticator{Store: store, Sessions: sessions, Dummy: dummy}, nil
}

func (a LocalAuthenticator) Authenticate(ctx context.Context, userName string, password []byte, remoteAddr string) (LocalAuthentication, error) {
	verification, err := a.Verify(ctx, userName, password, remoteAddr)
	if err != nil {
		return LocalAuthentication{}, err
	}
	defer clear(verification.Credential.Salt)
	defer clear(verification.Credential.Hash)
	return a.complete(ctx, verification, verification.ForceRotation)
}

func (a LocalAuthenticator) Verify(ctx context.Context, userName string, password []byte, remoteAddr string) (LocalVerification, error) {
	defer clear(password)
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	normalized := strings.ToLower(userName)
	accountKey, sourceKey := localThrottleKeys(normalized, canonicalRemoteAddress(remoteAddr))
	accountAllowed, _, accountErr := a.consume(ctx, accountKey, now)
	sourceAllowed, _, sourceErr := a.consume(ctx, sourceKey, now)
	if accountErr != nil || sourceErr != nil {
		a.denied(ctx, "error")
		return LocalVerification{}, ErrUnauthenticated
	}
	if !accountAllowed || !sourceAllowed {
		a.denied(ctx, "denied")
		return LocalVerification{}, &LoginThrottleError{RetryAfter: maxLoginRetryAfter}
	}

	userID, credential, lookupErr := a.Store.PasswordCredential(ctx, normalized)
	eligible := lookupErr == nil && credential.Validate() == nil
	if !eligible {
		clear(credential.Salt)
		clear(credential.Hash)
		credential = a.Dummy
	}
	verify := a.verify
	if verify == nil {
		verify = VerifyPassword
	}
	valid := verify(password, credential)
	if !eligible || !valid {
		if eligible {
			clear(credential.Salt)
			clear(credential.Hash)
		}
		a.denied(ctx, "denied")
		return LocalVerification{}, ErrUnauthenticated
	}
	salt, hash := bytes.Clone(credential.Salt), bytes.Clone(credential.Hash)
	clear(credential.Salt)
	clear(credential.Hash)
	credential.Salt, credential.Hash = salt, hash
	return LocalVerification{
		UserID: userID, ForceRotation: credential.ForceRotation, Credential: credential,
		accountKey: accountKey, sourceKey: sourceKey,
	}, nil
}

func (a LocalAuthenticator) denied(ctx context.Context, outcome string) {
	if a.Audit != nil {
		_ = a.Audit.Record(ctx, audit.Event{
			ActorType: "anonymous", TargetType: "authentication",
			AuthenticationMethod: "local", Operation: audit.OperationLocalLoginDenied,
			Outcome: outcome, RequestID: audit.RequestID(ctx),
		})
	}
}

func (a LocalAuthenticator) CompleteLogin(ctx context.Context, verification LocalVerification, prepared PreparedSession) (LocalAuthentication, error) {
	if verification.ForceRotation {
		return LocalAuthentication{}, ErrUnauthenticated
	}
	return a.completePrepared(ctx, verification, prepared)
}

func (a LocalAuthenticator) CompleteRotation(ctx context.Context, verification LocalVerification, prepared PreparedSession) (LocalAuthentication, error) {
	if !verification.ForceRotation {
		return LocalAuthentication{}, ErrUnauthenticated
	}
	return a.completePrepared(ctx, verification, prepared)
}

func (a LocalAuthenticator) completePrepared(ctx context.Context, verification LocalVerification, prepared PreparedSession) (LocalAuthentication, error) {
	if prepared.Record.UserID != verification.UserID || prepared.Record.Provider != "local" || prepared.Record.ForceRotation {
		return LocalAuthentication{}, ErrUnauthenticated
	}
	return LocalAuthentication{
		UserID: verification.UserID, Token: prepared.Token,
		ExpiresAt: prepared.ExpiresAt, ForceRotation: false,
	}, nil
}

func (a LocalAuthenticator) complete(ctx context.Context, verification LocalVerification, forceRotation bool) (LocalAuthentication, error) {
	if a.Sessions == nil {
		return LocalAuthentication{}, ErrUnauthenticated
	}
	prepared, err := a.Sessions.PrepareForUser(verification.UserID, "local", forceRotation)
	if err != nil {
		return LocalAuthentication{}, ErrUnauthenticated
	}
	creator, ok := a.Store.(interface {
		CreatePasswordSession(context.Context, int64, PasswordCredential, SessionRecord, [32]byte, [32]byte) error
	})
	if !ok {
		return LocalAuthentication{}, ErrUnauthenticated
	}
	if err := creator.CreatePasswordSession(ctx, verification.UserID, verification.Credential, prepared.Record, verification.accountKey, verification.sourceKey); err != nil {
		return LocalAuthentication{}, ErrUnauthenticated
	}
	return LocalAuthentication{UserID: verification.UserID, Token: prepared.Token, ExpiresAt: prepared.ExpiresAt, ForceRotation: forceRotation}, nil
}

func (a LocalAuthenticator) consume(ctx context.Context, key [32]byte, now time.Time) (bool, time.Time, error) {
	if a.Store == nil {
		return false, time.Time{}, errors.New("local store is not configured")
	}
	return a.Store.ConsumeLoginAttempt(ctx, key, now)
}

func localThrottleKeys(normalizedUserName, remoteAddr string) ([32]byte, [32]byte) {
	return sha256.Sum256([]byte("local-account\x00" + normalizedUserName)),
		sha256.Sum256([]byte("local-source\x00" + remoteAddr))
}

func canonicalRemoteAddress(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return address.Unmap().String()
	}
	return host
}
