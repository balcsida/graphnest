package authn

import (
	"context"
	"errors"
	"time"
)

// ProviderOAuthToken is the principal method for MCP clients authenticated with
// an OAuth access token issued by GraphNest's own authorization server. It is
// deliberately not an interactive method: such principals act as the user but
// may not mint further credentials.
const ProviderOAuthToken = "oauth_token"

// ErrOAuthReplay reports that a rotated refresh token was presented again
// outside the grace window. The store revokes the whole grant when this happens.
var ErrOAuthReplay = errors.New("refresh token replayed")

// ErrOAuthClientQuota reports that the deployment's registration capacity is full.
var ErrOAuthClientQuota = errors.New("oauth client registration limit reached")

// OAuthClient is a dynamically registered public MCP client.
type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
	LastUsedAt   time.Time
}

// OAuthAuthorizationRequest is a pending browser interaction: first awaiting
// consent (Phase "pending", keyed by the request handle), then awaiting code
// exchange (Phase "code", keyed by the authorization code).
type OAuthAuthorizationRequest struct {
	ID            [32]byte // sha256 of the request handle or authorization code
	Phase         string
	ClientID      string
	UserID        int64 // set when consent is granted
	RedirectURI   string
	CodeChallenge string
	State         string
	Scope         string
	Resource      string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// OAuthGrant is one user's authorization of one client. Refresh rotates the
// token hashes in place; the row is the revocable "connected app".
type OAuthGrant struct {
	ID                  int64
	ClientID            string
	UserID              int64
	Scope               string
	AccessHash          [32]byte
	AccessExpiresAt     time.Time
	RefreshHash         [32]byte
	PreviousRefreshHash *[32]byte
	// GitHubTokenCiphertext is the user's GitHub user-to-server token encrypted
	// by the authorization server; the store never sees plaintext.
	GitHubTokenCiphertext []byte
	CreatedAt             time.Time
	LastUsedAt            time.Time
	ExpiresAt             time.Time
	RevokedAt             *time.Time
}

// OAuthGrantMetadata is what an account owner sees about a grant.
type OAuthGrantMetadata struct {
	ID         int64
	ClientName string
	Scope      string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
}

// OAuthRotation carries the new token hashes and expiries for a refresh.
// Grace suppresses grant revocation for consumed-token retries after rotation.
// Such retries still fail; a lost refresh response requires fresh authorization.
type OAuthRotation struct {
	AccessHash      [32]byte
	AccessExpiresAt time.Time
	RefreshHash     [32]byte
	Now             time.Time
	Grace           time.Duration
}

// OAuthRequestLimiter enforces shared registration, token and revocation budgets.
type OAuthRequestLimiter interface {
	AllowOAuthRequest(ctx context.Context, remoteAddr, endpoint string, now time.Time) (bool, error)
}

// OAuthStore is the persistence contract of the authorization server.
type OAuthStore interface {
	OAuthRequestLimiter
	CreateOAuthClient(context.Context, OAuthClient) error
	OAuthClient(ctx context.Context, id string, now time.Time) (OAuthClient, error)

	CreateOAuthAuthorizationRequest(context.Context, OAuthAuthorizationRequest) error
	// OAuthAuthorizationRequest loads a live request without consuming it.
	OAuthAuthorizationRequest(ctx context.Context, id [32]byte, phase string, now time.Time) (OAuthAuthorizationRequest, error)
	// IssueOAuthCode consumes a pending request using the user's live consent session.
	IssueOAuthCode(ctx context.Context, pendingID, codeID, sessionHash [32]byte, userID int64, expiresAt, now time.Time) error
	DeleteOAuthAuthorizationRequest(ctx context.Context, id [32]byte) error
	// ExchangeOAuthCode atomically consumes a live code and creates its grant,
	// serialized with user credential revocation. Missing or used codes, mismatched
	// grant ownership/scope, and inactive users return pgx.ErrNoRows.
	ExchangeOAuthCode(ctx context.Context, codeID [32]byte, grant OAuthGrant) (int64, error)
	// OAuthPrincipal resolves a live access token to the user's principal and
	// bumps last_used_at.
	OAuthPrincipal(ctx context.Context, accessHash [32]byte, now time.Time) (Principal, error)
	// RotateOAuthGrant rotates the tokens of the grant owning refreshHash.
	// A consumed refresh token returns pgx.ErrNoRows without revoking the grant
	// within rotation.Grace of its consumption; at or after that deadline,
	// the grant is revoked and ErrOAuthReplay is returned.
	RotateOAuthGrant(ctx context.Context, refreshHash [32]byte, rotation OAuthRotation) (OAuthGrant, error)
	// OAuthGrantByRefresh loads the grant owning a current refresh token.
	OAuthGrantByRefresh(ctx context.Context, refreshHash [32]byte, now time.Time) (OAuthGrant, error)
	UpdateOAuthGrantGitHubToken(ctx context.Context, grantID int64, ciphertext []byte) error
	RevokeOAuthGrant(ctx context.Context, grantID int64) error
	// RevokeOAuthGrantByToken revokes the grant owning either token hash when
	// it belongs to clientID and reports whether a grant was newly revoked.
	// Unknown tokens are not an error (RFC 7009).
	RevokeOAuthGrantByToken(ctx context.Context, hash [32]byte, clientID string) (bool, error)
	ListOAuthGrants(ctx context.Context, userID, afterID int64, limit int) ([]OAuthGrantMetadata, bool, error)
	RevokeUserOAuthGrant(ctx context.Context, userID, grantID int64) error
	ReplaceGitHubGrants(ctx context.Context, userID int64, repositoryIDs []int64) error
}
