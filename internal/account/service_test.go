package account

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
)

type storeStub struct {
	authn.APITokenStore
	tokens                 []authn.APITokenMetadata
	revokedUser, revokedID int64
}

func (s *storeStub) CreateAPIToken(context.Context, authn.APITokenRecord) (int64, error) {
	return 7, nil
}
func (s *storeStub) ListAPITokens(context.Context, int64) ([]authn.APITokenMetadata, error) {
	return s.tokens, nil
}
func (s *storeStub) RevokeAPIToken(_ context.Context, user, id int64) error {
	s.revokedUser, s.revokedID = user, id
	return nil
}

func TestCreateTokenRejectsRepositoryOutsidePrincipalGrant(t *testing.T) {
	// Break caught: a user minting a token broader than their own grants.
	s := &Service{Manager: authn.TokenManager{Store: &storeStub{}, Rand: strings.NewReader(strings.Repeat("x", 32))}}
	_, _, err := s.CreateToken(t.Context(), authn.Principal{Subject: "11", Method: "oidc", RepositoryIDs: []int64{101}}, nil, []int64{102})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err=%v", err)
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
