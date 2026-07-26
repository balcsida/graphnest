package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/httpclient"
	"golang.org/x/oauth2"
)

var asymmetricAlgorithms = []string{
	coreoidc.RS256, coreoidc.RS384, coreoidc.RS512,
	coreoidc.PS256, coreoidc.PS384, coreoidc.PS512,
	coreoidc.ES256, coreoidc.ES384, coreoidc.ES512,
	coreoidc.EdDSA,
}

type Client struct {
	verifier         *coreoidc.IDTokenVerifier
	oauth            oauth2.Config
	http             *http.Client
	clientID         string
	groupsClaim      string
	displayNameClaim string
}

func New(
	ctx context.Context,
	cfg config.OIDC,
	publicURL *url.URL,
	clientSecret []byte,
	caPEM []byte,
) (*Client, error) {
	if slices.Contains(cfg.Scopes, coreoidc.ScopeOfflineAccess) {
		return nil, errors.New("offline_access is not supported")
	}
	httpClient, err := httpclient.New(caPEM)
	if err != nil {
		return nil, fmt.Errorf("build OIDC HTTP client: %w", err)
	}
	ctx = coreoidc.ClientContext(ctx, httpClient)
	provider, err := coreoidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	var metadata struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		JWKSURI               string `json:"jwks_uri"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return nil, fmt.Errorf("decode OIDC discovery: %w", err)
	}
	for name, endpoint := range map[string]string{
		"authorization": metadata.AuthorizationEndpoint,
		"token":         metadata.TokenEndpoint,
		"JWKS":          metadata.JWKSURI,
	} {
		if err := validateEndpoint(endpoint); err != nil {
			return nil, fmt.Errorf("invalid discovered %s endpoint: %w", name, err)
		}
	}
	callback := publicURL.ResolveReference(&url.URL{Path: "/auth/oidc/callback"})
	return &Client{
		verifier: provider.Verifier(&coreoidc.Config{
			ClientID:             cfg.ClientID,
			SupportedSigningAlgs: asymmetricAlgorithms,
		}),
		oauth: oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: string(clientSecret),
			Endpoint: provider.Endpoint(), RedirectURL: callback.String(), Scopes: cfg.Scopes,
		},
		http: httpClient, clientID: cfg.ClientID,
		groupsClaim: cfg.GroupsClaim, displayNameClaim: cfg.DisplayNameClaim,
	}, nil
}

func (client *Client) AuthorizationURL(state, nonce, verifier string) string {
	return client.oauth.AuthCodeURL(
		state,
		coreoidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	)
}

func (client *Client) Exchange(
	ctx context.Context,
	code, verifier, expectedNonce string,
) (authn.Identity, error) {
	ctx = coreoidc.ClientContext(ctx, client.http)
	token, err := client.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return authn.Identity{}, fmt.Errorf("exchange OIDC authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return authn.Identity{}, errors.New("OIDC token response lacks id_token")
	}
	idToken, err := client.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return authn.Identity{}, fmt.Errorf("verify OIDC ID token: %w", err)
	}
	if expectedNonce == "" || idToken.Nonce == "" || idToken.Nonce != expectedNonce {
		return authn.Identity{}, errors.New("OIDC ID token nonce mismatch")
	}
	if idToken.Subject == "" {
		return authn.Identity{}, errors.New("OIDC ID token lacks subject")
	}

	var claims map[string]json.RawMessage
	if err := idToken.Claims(&claims); err != nil {
		return authn.Identity{}, fmt.Errorf("decode verified OIDC claims: %w", err)
	}
	if err := validateAuthorizedParty(claims["azp"], idToken.Audience, client.clientID); err != nil {
		return authn.Identity{}, err
	}
	displayName, err := stringClaim(claims, client.displayNameClaim)
	if err != nil {
		return authn.Identity{}, err
	}
	groups, err := groupsClaim(claims, client.groupsClaim)
	if err != nil {
		return authn.Identity{}, err
	}
	return authn.Identity{
		Provider: "oidc", Issuer: idToken.Issuer, Subject: idToken.Subject,
		DisplayName: displayName, Groups: groups,
	}, nil
}

func validateEndpoint(raw string) error {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" ||
		endpoint.User != nil || endpoint.Fragment != "" {
		return errors.New("must be HTTPS without userinfo or fragment")
	}
	return nil
}

func validateAuthorizedParty(raw json.RawMessage, audience []string, clientID string) error {
	if len(raw) == 0 {
		if len(audience) > 1 {
			return errors.New("OIDC ID token with multiple audiences lacks azp")
		}
		return nil
	}
	var authorizedParty string
	if err := json.Unmarshal(raw, &authorizedParty); err != nil || authorizedParty != clientID {
		return errors.New("OIDC ID token azp mismatch")
	}
	return nil
}

func stringClaim(claims map[string]json.RawMessage, name string) (string, error) {
	if name == "" || len(claims[name]) == 0 {
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(claims[name]), []byte("null")) {
		return "", fmt.Errorf("OIDC claim %q must be a string", name)
	}
	var value string
	if err := json.Unmarshal(claims[name], &value); err != nil {
		return "", fmt.Errorf("OIDC claim %q must be a string", name)
	}
	return value, nil
}

func groupsClaim(claims map[string]json.RawMessage, name string) ([]string, error) {
	if name == "" || len(claims[name]) == 0 {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(claims[name]), []byte("null")) {
		return nil, fmt.Errorf("OIDC claim %q must be an array of strings", name)
	}
	var groups []string
	if err := json.Unmarshal(claims[name], &groups); err != nil {
		return nil, fmt.Errorf("OIDC claim %q must be an array of strings", name)
	}
	return groups, nil
}
