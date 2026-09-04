package authn

import (
	"errors"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidIdentity = errors.New("invalid identity")

const (
	ProviderOIDC  = "oidc"
	ProviderOAuth = "oauth"
	ProviderLocal = "local"
)

func IsInteractiveMethod(method string) bool {
	return method == ProviderOIDC || method == ProviderOAuth || method == ProviderLocal
}

type Identity struct {
	Provider, Issuer, Subject, LinkID, DisplayName string
	// Login is the provider's mutable account name; it is only used to name
	// just-in-time provisioned users and is never an identity.
	Login string
	// ProviderToken is the provider's user access token from the login that
	// produced this identity. It is only consulted when a browser login
	// continues an MCP authorization, so the authorization server can re-derive
	// access at refresh time; the session store never persists it.
	ProviderToken string
	// AccessSync, when non-nil, provisions the user on first login and replaces
	// that user's provider-derived repository grants with RepositoryIDs.
	AccessSync *AccessSync
}

type AccessSync struct{ RepositoryIDs []int64 }

func validIdentity(identity Identity) bool {
	return validIdentityField(identity.Provider) && validIdentityField(identity.Issuer) && validIdentityField(identity.Subject) && validIdentityField(identity.LinkID) && validIdentityDisplayName(identity.DisplayName) &&
		(identity.AccessSync == nil || validIdentityField(identity.Login))
}

func validIdentityField(value string) bool {
	if value == "" || !utf8.ValidString(value) || len(value) > 2048 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validIdentityDisplayName(value string) bool {
	if !utf8.ValidString(value) || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
