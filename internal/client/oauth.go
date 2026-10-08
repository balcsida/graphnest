package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/httpclient"
)

// loginScope matches authn.ScopeGraphWrite; package client must not import server packages.
const loginScope = "graph:write"

// Origin returns the scheme and lower-cased host of an http or https URL.
func Origin(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("GRAPHNEST_SERVER_URL must be an http or https URL")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// NewSecret returns 32 random bytes as 43 base64url characters, for PKCE verifiers and state.
func NewSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func newHTTPClient(config Config) (*http.Client, error) {
	httpClient, err := httpclient.New(config.CAPEM)
	if err != nil {
		return nil, err
	}
	httpClient.Timeout = config.Timeout
	if httpClient.Timeout == 0 {
		httpClient.Timeout = defaultTimeout
	}
	return httpClient, nil
}

// OAuth talks to one authorization server whose issuer was verified against the server origin.
type OAuth struct {
	http                                     *http.Client
	origin                                   string
	authorizationEndpoint, tokenEndpoint     string
	registrationEndpoint, revocationEndpoint string
}

// DiscoverOAuth fetches RFC 8414 metadata from the origin of config.ServerURL and checks issuer, PKCE S256 and endpoints.
func DiscoverOAuth(ctx context.Context, config Config) (*OAuth, error) {
	origin, err := Origin(config.ServerURL)
	if err != nil {
		return nil, err
	}
	httpClient, err := newHTTPClient(config)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/.well-known/oauth-authorization-server", nil)
	if err != nil {
		return nil, errors.New("build request failed")
	}
	request.Header.Set("Accept", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, transportError("discover authorization server", ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, errors.New("server does not offer OAuth sign-in (GRAPHNEST_MCP_OAUTH is off); set GRAPHNEST_TOKEN instead")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discover authorization server: server returned HTTP %d", response.StatusCode)
	}
	var metadata struct {
		Issuer                        string   `json:"issuer"`
		AuthorizationEndpoint         string   `json:"authorization_endpoint"`
		TokenEndpoint                 string   `json:"token_endpoint"`
		RegistrationEndpoint          string   `json:"registration_endpoint"`
		RevocationEndpoint            string   `json:"revocation_endpoint"`
		CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	}
	if json.NewDecoder(response.Body).Decode(&metadata) != nil {
		return nil, errors.New("authorization server metadata is invalid")
	}
	if metadata.Issuer != origin {
		return nil, fmt.Errorf("authorization server issuer %q does not match %q", metadata.Issuer, origin)
	}
	pkce := false
	for _, method := range metadata.CodeChallengeMethodsSupported {
		pkce = pkce || method == "S256"
	}
	if !pkce {
		return nil, errors.New("authorization server does not support PKCE S256")
	}
	if metadata.AuthorizationEndpoint == "" || metadata.TokenEndpoint == "" || metadata.RegistrationEndpoint == "" {
		return nil, errors.New("authorization server metadata lacks authorization, token or registration endpoint")
	}
	return &OAuth{http: httpClient, origin: origin, authorizationEndpoint: metadata.AuthorizationEndpoint, tokenEndpoint: metadata.TokenEndpoint,
		registrationEndpoint: metadata.RegistrationEndpoint, revocationEndpoint: metadata.RevocationEndpoint}, nil
}

// Register creates a public client (RFC 7591) for one login and returns its client_id.
func (o *OAuth) Register(ctx context.Context, redirectURI string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"client_name":                "graphnest CLI",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, o.registrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("build request failed")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := o.http.Do(request)
	if err != nil {
		return "", transportError("register client", ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("register client: server returned HTTP %d", response.StatusCode)
	}
	var registered struct {
		ClientID string `json:"client_id"`
	}
	if json.NewDecoder(response.Body).Decode(&registered) != nil || registered.ClientID == "" {
		return "", errors.New("register client: invalid response")
	}
	return registered.ClientID, nil
}

// AuthorizationURL builds the authorization request with an S256 challenge for verifier.
func (o *OAuth) AuthorizationURL(clientID, redirectURI, state, verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"scope":                 {loginScope},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	return o.authorizationEndpoint + "?" + query.Encode()
}

// Exchange trades an authorization code for a Login.
func (o *OAuth) Exchange(ctx context.Context, clientID, redirectURI, code, verifier string) (Login, error) {
	tokens, err := postTokenForm(ctx, o.http, o.tokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	})
	if err != nil {
		return Login{}, err
	}
	return Login{Server: o.origin, ClientID: clientID, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken,
		ExpiresAt: tokens.expiresAt, TokenEndpoint: o.tokenEndpoint, RevocationEndpoint: o.revocationEndpoint}, nil
}

// RevokeLogin revokes the login's refresh token (RFC 7009).
func RevokeLogin(ctx context.Context, config Config, login Login) error {
	if login.RevocationEndpoint == "" {
		return errors.New("the server has no revocation endpoint")
	}
	httpClient, err := newHTTPClient(config)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, login.RevocationEndpoint, strings.NewReader(url.Values{
		"token":           {login.RefreshToken},
		"token_type_hint": {"refresh_token"},
		"client_id":       {login.ClientID},
	}.Encode()))
	if err != nil {
		return errors.New("build request failed")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := httpClient.Do(request)
	if err != nil {
		return transportError("revoke login", ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("revoke login: server returned HTTP %d", response.StatusCode)
	}
	return nil
}

// oauthError is a token-endpoint error response.
type oauthError struct{ Code, Description string }

func (e *oauthError) Error() string {
	if e.Description == "" {
		return "token endpoint: " + e.Code
	}
	return "token endpoint: " + e.Code + ": " + e.Description
}

func isInvalidGrant(err error) bool {
	var oe *oauthError
	return errors.As(err, &oe) && oe.Code == "invalid_grant"
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
	expiresAt    time.Time
}

// postTokenForm posts a form to a token endpoint and returns a complete Bearer token response.
func postTokenForm(ctx context.Context, httpClient *http.Client, endpoint string, form url.Values) (tokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, errors.New("build request failed")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return tokenResponse{}, transportError("token request", ctx, err)
	}
	defer response.Body.Close()
	var tokens tokenResponse
	decodeErr := json.NewDecoder(response.Body).Decode(&tokens)
	if response.StatusCode != http.StatusOK {
		if decodeErr != nil || tokens.Error == "" {
			return tokenResponse{}, fmt.Errorf("token endpoint returned HTTP %d", response.StatusCode)
		}
		return tokenResponse{}, &oauthError{Code: tokens.Error, Description: tokens.Description}
	}
	if decodeErr != nil || tokens.AccessToken == "" || tokens.RefreshToken == "" || !strings.EqualFold(tokens.TokenType, "Bearer") || tokens.ExpiresIn <= 0 {
		return tokenResponse{}, errors.New("token endpoint returned an incomplete token response")
	}
	tokens.expiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	return tokens, nil
}

// transportError drops the URL from a transport error and prefers the context's error.
func transportError(what string, ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return fmt.Errorf("%s: %w", what, err)
}
