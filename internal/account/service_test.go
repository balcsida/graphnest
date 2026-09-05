package account

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/jackc/pgx/v5"
)

type storeStub struct {
	authn.APITokenStore
	tokens                 []authn.APITokenMetadata
	created                authn.APITokenRecord
	revokedUser, revokedID int64
}

func (s *storeStub) CreateAPIToken(_ context.Context, token authn.APITokenRecord) (int64, error) {
	s.created = token
	return 7, nil
}
func (s *storeStub) CreateAPITokenAudited(ctx context.Context, token authn.APITokenRecord, _ audit.Event) (int64, error) {
	return s.CreateAPIToken(ctx, token)
}
func (s *storeStub) ListAPITokens(context.Context, int64) ([]authn.APITokenMetadata, error) {
	return s.tokens, nil
}
func (s *storeStub) RevokeAPIToken(_ context.Context, user, id int64) error {
	s.revokedUser, s.revokedID = user, id
	return nil
}
func (s *storeStub) RevokeAPITokenAudited(ctx context.Context, user, id int64, _ audit.Event) error {
	return s.RevokeAPIToken(ctx, user, id)
}

func TestCreateTokenRejectsRepositoryOutsidePrincipalGrant(t *testing.T) {
	// Break caught: a user minting a token broader than their own grants.
	s := &Service{Manager: authn.TokenManager{Store: &storeStub{}, Rand: strings.NewReader(strings.Repeat("x", 32))}}
	expires := time.Now().Add(time.Hour)
	_, _, err := s.CreateToken(t.Context(), authn.Principal{Subject: "11", Method: "oidc", RepositoryIDs: []int64{101}}, &expires, []int64{102})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err=%v", err)
	}
}

func TestCreateTokenRequiresInteractiveSession(t *testing.T) {
	s := &Service{Manager: authn.TokenManager{Store: &storeStub{}, Rand: strings.NewReader(strings.Repeat("x", 32))}}
	if _, _, err := s.CreateToken(t.Context(), authn.Principal{
		Subject: "11", Method: "api_token", RepositoryIDs: []int64{101},
	}, nil, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("restricted API token minted child token: %v", err)
	}
}

func TestCredentialManagementRequiresInteractiveSession(t *testing.T) {
	for _, method := range []string{"oauth", "api_token"} {
		t.Run(method, func(t *testing.T) {
			store := &storeStub{tokens: []authn.APITokenMetadata{{ID: 7, Prefix: "gnp_abc"}}}
			service := &Service{Manager: authn.TokenManager{Store: store, Rand: strings.NewReader(strings.Repeat("x", 32))}}
			principal := authn.Principal{Subject: "11", Method: method}

			_, _, createErr := service.CreateToken(t.Context(), principal, nil, nil)
			_, listErr := service.Tokens(t.Context(), principal)
			revokeErr := service.RevokeToken(t.Context(), principal, 7)
			if method == "oauth" {
				if createErr != nil || listErr != nil || revokeErr != nil {
					t.Fatalf("OAuth credential management errors: create=%v list=%v revoke=%v", createErr, listErr, revokeErr)
				}
				return
			}
			if !errors.Is(createErr, ErrForbidden) || !errors.Is(listErr, ErrForbidden) || !errors.Is(revokeErr, ErrForbidden) {
				t.Fatalf("API token credential management errors: create=%v list=%v revoke=%v", createErr, listErr, revokeErr)
			}
		})
	}
}

func TestCreateTokenRequiresFutureExpiry(t *testing.T) {
	// Break caught: tokens created already expired or expiring exactly now.
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := &Service{Manager: authn.TokenManager{Store: &storeStub{}, Now: func() time.Time { return now }, Rand: strings.NewReader(strings.Repeat("x", 32))}}
	for _, expiry := range []time.Time{now.Add(-time.Second), now} {
		if _, _, err := s.CreateToken(t.Context(), authn.Principal{Subject: "11", Method: "oidc", RepositoryIDs: []int64{101}}, &expiry, []int64{101}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("expiry=%s err=%v", expiry, err)
		}
	}
	future := now.Add(time.Second)
	token, _, err := s.CreateToken(t.Context(), authn.Principal{Subject: "11", Method: "oidc", RepositoryIDs: []int64{101}}, &future, []int64{101})
	if err != nil || token.ExpiresAt == nil || !token.ExpiresAt.Equal(future) {
		t.Fatalf("token=%#v err=%v", token, err)
	}
}

func TestCreateTokenAllowsOptionalControlsForOrdinaryUsers(t *testing.T) {
	store := &storeStub{}
	s := &Service{Manager: authn.TokenManager{Store: store, Rand: strings.NewReader(strings.Repeat("x", 32))}}
	token, _, err := s.CreateToken(t.Context(), authn.Principal{Subject: "11", Method: "oidc"}, nil, nil)
	if err != nil || token.ExpiresAt != nil || token.RepositoryIDs != nil ||
		store.created.ExpiresAt != nil || store.created.RepositoryIDs != nil {
		t.Fatalf("token=%#v stored=%#v err=%v", token, store.created, err)
	}
}

func TestCreateTokenRequiresAdministratorCeilingAndBoundsExpiry(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := &Service{Manager: authn.TokenManager{Store: &storeStub{}, Now: func() time.Time { return now }, Rand: strings.NewReader(strings.Repeat("x", 32))}}
	admin := authn.Principal{Subject: "11", Method: "oidc", Administrator: true}
	if _, _, err := s.CreateToken(t.Context(), admin, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty administrator ceiling err=%v", err)
	}
	tooLate := now.Add(90*24*time.Hour + time.Second)
	if _, _, err := s.CreateToken(t.Context(), authn.Principal{Subject: "11", Method: "oidc"}, &tooLate, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overlong expiry err=%v", err)
	}
}

func TestTokensReturnMetadataWithoutSecret(t *testing.T) {
	// Break caught: list endpoint source leaking token hash or plaintext.
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := &Service{Manager: authn.TokenManager{Store: &storeStub{tokens: []authn.APITokenMetadata{{ID: 7, Prefix: "gnp_abc", RepositoryIDs: []int64{101}, CreatedAt: now}}}}}
	got, err := s.Tokens(t.Context(), authn.Principal{Subject: "11", Method: "oidc"})
	if err != nil || len(got) != 1 || got[0].ID != 7 || got[0].Prefix != "gnp_abc" || len(got[0].RepositoryIDs) != 1 || got[0].RepositoryIDs[0] != 101 {
		t.Fatalf("tokens=%#v err=%v", got, err)
	}
}

func TestRevokeTokenUsesAuthenticatedOwner(t *testing.T) {
	// Break caught: revocation using an arbitrary user ID rather than the caller.
	store := &storeStub{}
	s := &Service{Manager: authn.TokenManager{Store: store}}
	if err := s.RevokeToken(t.Context(), authn.Principal{Subject: "11", Method: "oidc"}, 9); err != nil {
		t.Fatal(err)
	}
	if store.revokedUser != 11 || store.revokedID != 9 {
		t.Fatalf("revoked user=%d id=%d", store.revokedUser, store.revokedID)
	}
}

func TestDelegateMintsNarrowedTokenForAdministratorAPIToken(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	store := &storeStub{}
	s := &Service{Manager: authn.TokenManager{Store: store, Now: func() time.Time { return now }, Rand: strings.NewReader(strings.Repeat("x", 32))}}
	admin := authn.Principal{Subject: "11", Method: "api_token", Administrator: true, RepositoryIDs: []int64{101, 102}}
	expires := now.Add(15 * time.Minute)
	token, plaintext, err := s.Delegate(t.Context(), admin, &expires, []int64{101})
	if err != nil || plaintext == "" || token.ExpiresAt == nil || !token.ExpiresAt.Equal(expires) {
		t.Fatalf("token=%#v plaintext=%q err=%v", token, plaintext, err)
	}
	if store.created.UserID != 11 || len(store.created.RepositoryIDs) != 1 || store.created.RepositoryIDs[0] != 101 || store.created.ExpiresAt == nil {
		t.Fatalf("stored=%#v", store.created)
	}
}

func TestDelegateRequiresAdministratorAPIToken(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := &Service{Manager: authn.TokenManager{Store: &storeStub{}, Now: func() time.Time { return now }, Rand: strings.NewReader(strings.Repeat("x", 32))}}
	expires := now.Add(15 * time.Minute)
	for name, principal := range map[string]authn.Principal{
		"ordinary api token": {Subject: "11", Method: "api_token", RepositoryIDs: []int64{101}},
		"interactive admin":  {Subject: "11", Method: "oidc", Administrator: true, RepositoryIDs: []int64{101}},
		"static token":       {Method: "static", Administrator: true, RepositoryIDs: []int64{101}},
	} {
		if _, _, err := s.Delegate(t.Context(), principal, &expires, []int64{101}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s: err=%v, want ErrForbidden", name, err)
		}
	}
}

func TestDelegateRequiresCeilingWithinGrantAndShortExpiry(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := &Service{Manager: authn.TokenManager{Store: &storeStub{}, Now: func() time.Time { return now }, Rand: strings.NewReader(strings.Repeat("x", 32))}}
	admin := authn.Principal{Subject: "11", Method: "api_token", Administrator: true, RepositoryIDs: []int64{101}}
	soon := now.Add(15 * time.Minute)
	if _, _, err := s.Delegate(t.Context(), admin, &soon, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty ceiling err=%v", err)
	}
	if _, _, err := s.Delegate(t.Context(), admin, nil, []int64{101}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing expiry err=%v", err)
	}
	tooLate := now.Add(MaxDelegatedTokenLifetime + time.Second)
	if _, _, err := s.Delegate(t.Context(), admin, &tooLate, []int64{101}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overlong expiry err=%v", err)
	}
	past := now.Add(-time.Second)
	if _, _, err := s.Delegate(t.Context(), admin, &past, []int64{101}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("past expiry err=%v", err)
	}
	if _, _, err := s.Delegate(t.Context(), admin, &soon, []int64{999}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("repository outside the caller's ceiling err=%v", err)
	}
}

// MCP OAuth access tokens act as the user but must never mint or manage
// long-lived credentials: a leaked hour-long token stays an hour-long token.
func TestOAuthAccessTokensCannotManageCredentials(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := &Service{Manager: authn.TokenManager{Store: &storeStub{}, Now: func() time.Time { return now }, Rand: strings.NewReader(strings.Repeat("x", 32))}}
	principal := authn.Principal{Subject: "11", Method: authn.ProviderOAuthToken, Administrator: true, RepositoryIDs: []int64{101}}
	expires := now.Add(15 * time.Minute)
	if _, _, err := s.CreateToken(t.Context(), principal, &expires, []int64{101}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("CreateToken err=%v", err)
	}
	if _, _, err := s.Delegate(t.Context(), principal, &expires, []int64{101}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Delegate err=%v", err)
	}
	if _, err := s.Tokens(t.Context(), principal); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Tokens err=%v", err)
	}
	if err := s.RevokeToken(t.Context(), principal, 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("RevokeToken err=%v", err)
	}
}

type grantStoreStub struct {
	truncated bool
	listed    [3]int64
	storeStub
	grants    []authn.OAuthGrantMetadata
	revoked   [][2]int64
	events    []audit.Event
	revokeErr error
}

func (s *grantStoreStub) ListOAuthGrants(_ context.Context, userID, afterID int64, limit int) ([]authn.OAuthGrantMetadata, bool, error) {
	s.listed = [3]int64{userID, afterID, int64(limit)}
	return s.grants, s.truncated, nil
}
func (s *grantStoreStub) RevokeUserOAuthGrantAudited(_ context.Context, userID, grantID int64, event audit.Event) error {
	s.revoked = append(s.revoked, [2]int64{userID, grantID})
	s.events = append(s.events, event)
	if s.revokeErr != nil {
		return s.revokeErr
	}
	for _, grant := range s.grants {
		if grant.ID == grantID {
			return nil
		}
	}
	return pgx.ErrNoRows
}

func TestGrantsAreListedAndRevokedByInteractiveOwnersOnly(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := &grantStoreStub{grants: []authn.OAuthGrantMetadata{{ID: 5, ClientName: "OpenCode", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)}}, truncated: true}
	s := &Service{Manager: authn.TokenManager{Store: store}}
	owner := authn.Principal{Subject: "11", Method: "oauth"}
	ctx := audit.WithRequestID(t.Context(), "request-42")
	grants, truncated, err := s.Grants(t.Context(), owner, 17)
	if err != nil || len(grants) != 1 || grants[0].ClientName != "OpenCode" || grants[0].ID != 5 || !truncated {
		t.Fatalf("grants=%+v truncated=%t err=%v", grants, truncated, err)
	}
	if store.listed != [3]int64{11, 17, 100} {
		t.Fatalf("ListOAuthGrants arguments=%v", store.listed)
	}
	if _, _, err := s.Grants(t.Context(), authn.Principal{Subject: "11", Method: authn.ProviderOAuthToken}, 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("access token listing err=%v", err)
	}
	if err := s.RevokeGrant(ctx, owner, 5); err != nil || len(store.revoked) != 1 || store.revoked[0] != [2]int64{11, 5} {
		t.Fatalf("revoke err=%v revoked=%v", err, store.revoked)
	}
	if len(store.events) != 1 || store.events[0] != (audit.Event{
		ActorType: "user", ActorID: "11", TargetType: "oauth_grant", TargetID: "5",
		AuthenticationMethod: "oauth", Operation: audit.OperationOAuthGrantRevoked,
		Outcome: "success", RequestID: "request-42",
	}) {
		t.Fatalf("revoke event=%#v", store.events)
	}
	if err := s.RevokeGrant(t.Context(), owner, 6); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unknown grant err=%v", err)
	}
	if err := s.RevokeGrant(t.Context(), authn.Principal{Subject: "11", Method: "api_token"}, 5); !errors.Is(err, ErrForbidden) {
		t.Fatalf("api token revoke err=%v", err)
	}
	auditFailure := errors.New("audit failed")
	store.revokeErr = auditFailure
	if err := s.RevokeGrant(t.Context(), owner, 5); !errors.Is(err, auditFailure) {
		t.Fatalf("audit failure err=%v", err)
	}

	plain := &Service{Manager: authn.TokenManager{Store: &storeStub{}}}
	if grants, truncated, err := plain.Grants(t.Context(), owner, 0); err != nil || len(grants) != 0 || truncated {
		t.Fatalf("store without grants: %+v truncated=%t err=%v", grants, truncated, err)
	}
}
