package oauthas

import (
	"context"
	"sync"
	"time"
)

// ProviderTokens is the in-memory hand-off between a browser login that
// continues an MCP authorization and the code exchange that follows it. Login
// deposits the identity provider's user token under the pending request; consent
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
	subject string
	token   string
	expires time.Time
}

func NewProviderTokens(now func() time.Time) *ProviderTokens {
	if now == nil {
		now = time.Now
	}
	return &ProviderTokens{now: now, entries: map[[32]byte]providerTokenEntry{}}
}

// Deposit implements browserflow.ProviderTokenSink. The validated login binds
// its provider token and trusted GraphNest subject to one pending request.
func (p *ProviderTokens) Deposit(_ context.Context, requestHash [32]byte, subject, providerToken string) {
	if subject == "" || providerToken == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep()
	p.entries[requestHash] = providerTokenEntry{subject: subject, token: providerToken, expires: p.now().Add(pendingTTL)}
}

// Available checks a pending handoff without consuming it.
func (p *ProviderTokens) Available(requestHash [32]byte, subject string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep()
	entry, found := p.entries[requestHash]
	return found && entry.subject == subject
}

// Transfer moves an owner-checked pending handoff to an authorization code.
func (p *ProviderTokens) Transfer(requestHash [32]byte, subject string, codeHash [32]byte) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep()
	entry, found := p.entries[requestHash]
	if !found || entry.subject != subject {
		return false
	}
	delete(p.entries, requestHash)
	p.entries[codeHash] = providerTokenEntry{subject: subject, token: entry.token, expires: p.now().Add(codeTTL)}
	return true
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

func (p *ProviderTokens) sweep() {
	now := p.now()
	for key, entry := range p.entries {
		if !entry.expires.After(now) {
			delete(p.entries, key)
		}
	}
}
