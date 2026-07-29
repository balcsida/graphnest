package authn

import (
	"context"
	"time"
)

type LoginFlow struct {
	StateHash, BrowserHash                  [32]byte
	Provider, Nonce, CodeVerifier, ReturnTo string
	CreatedAt, ExpiresAt                    time.Time
}

type SessionRecord struct {
	TokenHash                                       [32]byte
	UserID                                          int64
	Provider                                        string
	ForceRotation                                   bool
	CreatedAt, LastSeenAt, IdleExpiresAt, ExpiresAt time.Time
}

type SessionStore interface {
	BindOIDCUser(context.Context, string, string, string) (int64, error)
	CreateLoginFlow(context.Context, LoginFlow) error
	ConsumeLoginFlow(context.Context, [32]byte, [32]byte, string, time.Time) (LoginFlow, error)
	CreateSession(context.Context, SessionRecord) error
	SessionPrincipal(context.Context, [32]byte, time.Time, time.Time) (Principal, error)
	RevokeSession(context.Context, [32]byte) error
	DeleteExpiredAuth(context.Context, time.Time) (flows, sessions int64, err error)
}
