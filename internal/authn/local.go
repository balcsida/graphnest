package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"
)

const maxLoginRetryAfter = 15 * time.Minute

type LocalStore interface {
	PasswordCredential(context.Context, string) (int64, PasswordCredential, error)
	ConsumeLoginAttempt(context.Context, [32]byte, time.Time) (bool, time.Time, error)
	ClearLoginFailures(context.Context, [32]byte) error
}

type LocalAuthentication struct {
	Token         string
	ExpiresAt     time.Time
	ForceRotation bool
}

type LoginThrottleError struct{ RetryAfter time.Duration }

func (e *LoginThrottleError) Error() string { return "login throttled" }

type LocalAuthenticator struct {
	Store    LocalStore
	Sessions *SessionManager
	Now      func() time.Time
	Dummy    PasswordCredential
	verify   func([]byte, PasswordCredential) bool
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
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	normalized := strings.ToLower(userName)
	accountKey, sourceKey := localThrottleKeys(normalized, canonicalRemoteAddress(remoteAddr))
	accountAllowed, accountRetry, accountErr := a.consume(ctx, accountKey, now)
	sourceAllowed, sourceRetry, sourceErr := a.consume(ctx, sourceKey, now)
	if accountErr != nil || sourceErr != nil {
		clear(password)
		return LocalAuthentication{}, ErrUnauthenticated
	}
	if !accountAllowed || !sourceAllowed {
		clear(password)
		retryAt := accountRetry
		if sourceRetry.After(retryAt) {
			retryAt = sourceRetry
		}
		retryAfter := retryAt.Sub(now)
		if retryAfter < time.Second {
			retryAfter = time.Second
		} else if retryAfter > maxLoginRetryAfter {
			retryAfter = maxLoginRetryAfter
		}
		return LocalAuthentication{}, &LoginThrottleError{RetryAfter: retryAfter}
	}

	userID, credential, lookupErr := a.Store.PasswordCredential(ctx, normalized)
	eligible := lookupErr == nil && credential.Validate() == nil
	if !eligible {
		credential = a.Dummy
	}
	verify := a.verify
	if verify == nil {
		verify = VerifyPassword
	}
	valid := verify(password, credential)
	if !eligible || !valid {
		return LocalAuthentication{}, ErrUnauthenticated
	}
	accountClearErr := a.Store.ClearLoginFailures(ctx, accountKey)
	sourceClearErr := a.Store.ClearLoginFailures(ctx, sourceKey)
	if accountClearErr != nil || sourceClearErr != nil || a.Sessions == nil {
		return LocalAuthentication{}, ErrUnauthenticated
	}
	token, expiresAt, err := a.Sessions.CreateForUser(ctx, userID, "local", credential.ForceRotation)
	if err != nil {
		return LocalAuthentication{}, ErrUnauthenticated
	}
	return LocalAuthentication{Token: token, ExpiresAt: expiresAt, ForceRotation: credential.ForceRotation}, nil
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
