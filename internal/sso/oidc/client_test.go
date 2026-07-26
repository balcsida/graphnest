package oidc_test

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/config"
	oidcclient "github.com/grepnest/grepnest/internal/sso/oidc"
	"golang.org/x/oauth2"
)

type providerFixture struct {
	server       *httptest.Server
	key          *rsa.PrivateKey
	issuer       string
	discovery    map[string]any
	tokenRequest url.Values
	mu           sync.Mutex
}

func newProvider(t *testing.T) *providerFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &providerFixture{key: key}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(fixture.serveHTTP))
	fixture.issuer = fixture.server.URL
	fixture.discovery = map[string]any{
		"issuer":                                fixture.issuer,
		"authorization_endpoint":                fixture.issuer + "/authorize",
		"token_endpoint":                        fixture.issuer + "/token",
		"jwks_uri":                              fixture.issuer + "/keys",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	}
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (fixture *providerFixture) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(writer).Encode(fixture.discovery)
	case "/keys":
		n := base64.RawURLEncoding.EncodeToString(fixture.key.N.Bytes())
		exponent := big.NewInt(int64(fixture.key.E)).Bytes()
		_ = json.NewEncoder(writer).Encode(map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": "test", "use": "sig", "alg": "RS256", "n": n,
			"e": base64.RawURLEncoding.EncodeToString([]byte(exponent)),
		}}})
	case "/token":
		_ = request.ParseForm()
		writer.Header().Set("Content-Type", "application/json")
		fixture.mu.Lock()
		fixture.tokenRequest = request.Form
		fixture.mu.Unlock()
		if request.Form.Get("code") == "no-token" {
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "secret", "token_type": "Bearer"})
			return
		}
		claims := fixture.claims(request.Form.Get("code"))
		token := fixture.sign(tHeader(request.Form.Get("code")), claims)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"access_token": "access-secret", "refresh_token": "refresh-secret",
			"token_type": "Bearer", "expires_in": 300, "id_token": token,
		})
	default:
		http.NotFound(writer, request)
	}
}

func (fixture *providerFixture) claims(code string) map[string]any {
	claims := map[string]any{
		"iss": fixture.issuer, "sub": "user-123", "aud": "client-id",
		"exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(),
		"nonce": "expected-nonce", "name": "Test User",
		"groups": []string{"engineering", "platform"},
	}
	switch code {
	case "wrong-nonce":
		claims["nonce"] = "wrong"
	case "missing-nonce":
		delete(claims, "nonce")
	case "wrong-audience":
		claims["aud"] = "other"
	case "multi-no-azp":
		claims["aud"] = []string{"client-id", "other"}
	case "multi-wrong-azp":
		claims["aud"], claims["azp"] = []string{"client-id", "other"}, "other"
	case "wrong-azp":
		claims["azp"] = "other"
	case "wrong-issuer":
		claims["iss"] = "https://wrong.example"
	case "expired":
		claims["exp"] = time.Now().Add(-time.Minute).Unix()
	case "missing-subject":
		delete(claims, "sub")
	case "bad-name":
		claims["name"] = []string{"bad"}
	case "null-name":
		claims["name"] = nil
	case "bad-groups":
		claims["groups"] = []any{"engineering", 7}
	case "null-groups":
		claims["groups"] = nil
	}
	return claims
}

func tHeader(code string) map[string]any {
	switch code {
	case "unsigned":
		return map[string]any{"alg": "none", "kid": "test", "typ": "JWT"}
	case "hs-signed":
		return map[string]any{"alg": "HS256", "kid": "test", "typ": "JWT"}
	default:
		return map[string]any{"alg": "RS256", "kid": "test", "typ": "JWT"}
	}
}

func (fixture *providerFixture) sign(header, claims map[string]any) string {
	encoded := func(value any) string {
		data, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(data)
	}
	payload := encoded(header) + "." + encoded(claims)
	sum := sha256.Sum256([]byte(payload))
	switch header["alg"] {
	case "none":
		return payload + "."
	case "HS256":
		mac := hmac.New(sha256.New, []byte("secret"))
		_, _ = mac.Write([]byte(payload))
		return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	default:
		signature, _ := rsa.SignPKCS1v15(rand.Reader, fixture.key, crypto.SHA256, sum[:])
		return payload + "." + base64.RawURLEncoding.EncodeToString(signature)
	}
}

func (fixture *providerFixture) caPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.server.Certificate().Raw})
}

func (fixture *providerFixture) client(t *testing.T) *oidcclient.Client {
	t.Helper()
	publicURL, _ := url.Parse("https://grepnest.example/")
	client, err := oidcclient.New(t.Context(), config.OIDC{
		IssuerURL: fixture.issuer, ClientID: "client-id",
		Scopes:      []string{"openid", "profile", "email"},
		GroupsClaim: "groups", DisplayNameClaim: "name",
	}, publicURL, []byte("client-secret"), fixture.caPEM())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestClientDiscoveryAndAuthorizationURL(t *testing.T) {
	fixture := newProvider(t)
	client := fixture.client(t)
	authorizationURL, err := url.Parse(client.AuthorizationURL("state-value", "expected-nonce", "verifier-value"))
	if err != nil {
		t.Fatal(err)
	}
	query := authorizationURL.Query()
	expected := map[string]string{
		"client_id": "client-id", "redirect_uri": "https://grepnest.example/auth/oidc/callback",
		"scope": "openid profile email", "state": "state-value", "nonce": "expected-nonce",
		"response_type": "code", "code_challenge_method": "S256",
		"code_challenge": oauth2.S256ChallengeFromVerifier("verifier-value"),
	}
	for name, value := range expected {
		if query.Get(name) != value {
			t.Errorf("%s = %q, want %q", name, query.Get(name), value)
		}
	}
}

func TestClientConstructorRejectsInvalidDiscovery(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*providerFixture)
	}{
		{"issuer mismatch", func(f *providerFixture) { f.discovery["issuer"] = "https://wrong.example" }},
		{"http authorization", func(f *providerFixture) { f.discovery["authorization_endpoint"] = "http://idp.example/authorize" }},
		{"http token", func(f *providerFixture) { f.discovery["token_endpoint"] = "http://idp.example/token" }},
		{"http jwks", func(f *providerFixture) { f.discovery["jwks_uri"] = "http://idp.example/keys" }},
		{"authorization fragment", func(f *providerFixture) { f.discovery["authorization_endpoint"] = f.issuer + "/authorize#bad" }},
		{"token userinfo", func(f *providerFixture) { f.discovery["token_endpoint"] = "https://user@example.test/token" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProvider(t)
			test.change(fixture)
			publicURL, _ := url.Parse("https://grepnest.example/")
			if _, err := oidcclient.New(t.Context(), config.OIDC{
				IssuerURL: fixture.issuer, ClientID: "client-id", Scopes: []string{"openid"},
			}, publicURL, []byte("secret"), fixture.caPEM()); err == nil {
				t.Fatal("constructor succeeded")
			}
		})
	}
	t.Run("untrusted CA", func(t *testing.T) {
		fixture := newProvider(t)
		publicURL, _ := url.Parse("https://grepnest.example/")
		if _, err := oidcclient.New(t.Context(), config.OIDC{
			IssuerURL: fixture.issuer, ClientID: "client-id", Scopes: []string{"openid"},
		}, publicURL, []byte("secret"), nil); err == nil {
			t.Fatal("constructor succeeded")
		}
	})
	t.Run("offline access", func(t *testing.T) {
		fixture := newProvider(t)
		publicURL, _ := url.Parse("https://grepnest.example/")
		if _, err := oidcclient.New(t.Context(), config.OIDC{
			IssuerURL: fixture.issuer, ClientID: "client-id",
			Scopes: []string{"openid", "offline_access"},
		}, publicURL, []byte("secret"), fixture.caPEM()); err == nil {
			t.Fatal("constructor succeeded")
		}
	})
}

func TestClientExchange(t *testing.T) {
	fixture := newProvider(t)
	client := fixture.client(t)
	identity, err := client.Exchange(t.Context(), "valid-code", "verifier-value", "expected-nonce")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Provider != "oidc" || identity.Issuer != fixture.issuer ||
		identity.Subject != "user-123" || identity.DisplayName != "Test User" ||
		!slices.Equal(identity.Groups, []string{"engineering", "platform"}) {
		t.Fatalf("identity = %#v", identity)
	}
	fixture.mu.Lock()
	request := fixture.tokenRequest
	fixture.mu.Unlock()
	if request.Get("code_verifier") != "verifier-value" ||
		request.Get("redirect_uri") != "https://grepnest.example/auth/oidc/callback" {
		t.Fatalf("token request = %v", request)
	}
}

func TestClientExchangeRejectsInvalidIDTokens(t *testing.T) {
	for _, code := range []string{
		"wrong-nonce", "missing-nonce", "wrong-audience", "multi-no-azp",
		"multi-wrong-azp", "wrong-azp", "wrong-issuer", "expired", "unsigned",
		"hs-signed", "missing-subject", "bad-name", "bad-groups", "no-token",
		"null-name", "null-groups",
	} {
		t.Run(code, func(t *testing.T) {
			fixture := newProvider(t)
			if _, err := fixture.client(t).Exchange(context.Background(), code, "verifier", "expected-nonce"); err == nil {
				t.Fatal("exchange succeeded")
			}
		})
	}
}
