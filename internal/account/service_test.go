package account

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/audit"
	"github.com/grepnest/grepnest/internal/authn"
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
