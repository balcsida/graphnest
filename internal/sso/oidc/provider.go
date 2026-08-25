package oidc

import (
	"time"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/sso"
	"github.com/balcsida/graphnest/internal/sso/browserflow"
)

const OIDCLoginCookieName = sso.OIDCLoginCookieName

func NewProvider(client browserflow.Client, store authn.SessionStore, sessions *authn.SessionManager, recorder audit.Recorder, loginTTL time.Duration) *browserflow.Provider {
	return &browserflow.Provider{
		Spec: browserflow.Spec{
			Metadata:  sso.Metadata{ID: "oidc", Label: "Sign in with SSO", LoginURL: "/auth/oidc/login"},
			LoginPath: "/auth/oidc/login", CallbackPath: "/auth/oidc/callback",
			FlowProvider: authn.ProviderOIDC, IdentityProvider: authn.ProviderOIDC,
			CookieName: sso.OIDCLoginCookieName, Method: authn.ProviderOIDC,
			SuccessOperation: audit.OperationOIDCLoginSucceeded, DeniedOperation: audit.OperationOIDCLoginDenied,
		},
		Client: client, Store: store, Sessions: sessions, Audit: recorder, LoginTTL: loginTTL,
	}
}
