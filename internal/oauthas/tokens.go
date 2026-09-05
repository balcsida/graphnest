package oauthas

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"time"
)

// ProviderTokens is the in-memory hand-off between a browser login that
// continues an MCP authorization and the code exchange that follows it. Login
// deposits the identity provider's user token under the new session; consent
// moves it to the authorization code; exchange takes it and seals it into the
// grant. Entries expire with the authorization request, so nothing outlives
// the flow and nothing is ever written to disk in plaintext.
//
// ponytail: a process-local map. A multi-replica deployment needs the login
// and the exchange to hit the same replica, or a shared sealed store.
type ProviderTokens struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[[32]byte]providerTokenEntry
}

type providerTokenEntry struct {
	token   string
	expires time.Time
}

func NewProviderTokens(now func() time.Time) *ProviderTokens {
	if now == nil {
		now = time.Now
	}
	return &ProviderTokens{now: now, entries: map[[32]byte]providerTokenEntry{}}
}

// StoreProviderToken implements browserflow.ProviderTokenSink: the login
// keys the token by the browser session it just created.
func (p *ProviderTokens) StoreProviderToken(_ context.Context, sessionToken, providerToken string) {
	key, ok := sessionKey(sessionToken)
	if !ok || providerToken == "" {
		return
	}
	p.put(key, providerToken, pendingTTL)
}

// Transfer moves a token deposited under a session to an authorization code.
func (p *ProviderTokens) Transfer(sessionToken string, codeHash [32]byte) {
	key, ok := sessionKey(sessionToken)
	if !ok {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep()
	entry, found := p.entries[key]
	if !found {
		return
	}
	delete(p.entries, key)
	p.entries[codeHash] = providerTokenEntry{token: entry.token, expires: p.now().Add(codeTTL)}
}

// TokenForCode returns the token attached to a live authorization code.
func (p *ProviderTokens) TokenForCode(codeHash [32]byte) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep()
	entry, found := p.entries[codeHash]
	if !found {
		return "", false
	}
	return entry.token, true
}

// DeleteForCode removes the token after its authorization code is consumed.
func (p *ProviderTokens) DeleteForCode(codeHash [32]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.entries, codeHash)
}

func (p *ProviderTokens) put(key [32]byte, token string, ttl time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep()
	p.entries[key] = providerTokenEntry{token: token, expires: p.now().Add(ttl)}
}

func (p *ProviderTokens) sweep() {
	now := p.now()
	for key, entry := range p.entries {
		if !entry.expires.After(now) {
			delete(p.entries, key)
		}
	}
}

// sessionKey hashes a session cookie value the same way the session store
// does, so the plaintext session token is never held here.
func sessionKey(sessionToken string) ([32]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(sessionToken)
	if err != nil || len(raw) != 32 {
		return [32]byte{}, false
	}
	return sha256.Sum256(raw), true
}
