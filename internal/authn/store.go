package authn

import (
	"context"
	"time"
)

type LoginFlow struct {
	StateHash    [32]byte
	BrowserHash  [32]byte
	Provider     string
	Nonce        string
	CodeVerifier string
	ReturnTo     string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

type SessionRecord struct {
	TokenHash   [32]byte
	Provider    string
	DisplayName string
	Principal   Principal
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

type SessionStore interface {
	CreateLoginFlow(context.Context, LoginFlow) error
	ConsumeLoginFlow(context.Context, [32]byte, [32]byte, string, time.Time) (LoginFlow, error)
	CreateSession(context.Context, SessionRecord) error
	Session(context.Context, [32]byte, time.Time) (SessionRecord, error)
	DeleteSession(context.Context, [32]byte) error
	DeleteExpiredAuth(context.Context, time.Time) (flows, sessions int64, err error)
}
