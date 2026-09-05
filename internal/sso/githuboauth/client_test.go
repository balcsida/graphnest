package githuboauth

import (
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/githubapp"
	"golang.org/x/oauth2"
)

const (
	testCode   = "code-canary"
	testSecret = "secret-canary"
	testToken  = "token-canary"
	bodyCanary = "body-canary"
)

func TestNewClientValidatesAndCanonicalizesWebOrigin(t *testing.T) {
	invalid := []string{
		"http://github.example", "https://user@github.example", "https://github.example/?q=1",
		"https://github.example/#fragment", "https://github.example/enterprise",
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			endpoints := endpointsFor(t, raw, "https://api.github.example")
			if _, err := NewClient(endpoints, mustURL(t, "https://graphnest.example"), "client", []byte(testSecret), "v1", http.DefaultClient); err == nil {
				t.Fatal("NewClient accepted invalid web origin")
			}
		})
	}

	for _, test := range []struct{ raw, issuer string }{
		{"https://GITHUB.EXAMPLE/", "https://github.example"},
		{"https://GITHUB.EXAMPLE:443", "https://github.example"},
		{"https://GITHUB.EXAMPLE:8443/", "https://github.example:8443"},
	} {
		t.Run(test.raw, func(t *testing.T) {
			client, err := NewClient(endpointsFor(t, test.raw, "https://api.github.example"), mustURL(t, "https://graphnest.example"), "client", []byte(testSecret), "v1", http.DefaultClient)
			if err != nil {
				t.Fatal(err)
			}
			if client.issuer != test.issuer {
				t.Fatalf("issuer = %q, want %q", client.issuer, test.issuer)
			}
		})
	}
}

func TestAuthorizationURLUsesFixedEndpointCallbackAndPKCEWithoutScope(t *testing.T) {
	fixture := newFixture(t, nil)
	got, err := url.Parse(fixture.client.AuthorizationURL("exact-state", "ignored-nonce", "exact-verifier"))
	if err != nil {
		t.Fatal(err)
	}
	query := got.Query()
	wantChallenge := oauth2.S256ChallengeFromVerifier("exact-verifier")
	if got.Path != "/login/oauth/authorize" || query.Get("state") != "exact-state" || query.Get("redirect_uri") != "https://graphnest.example/auth/oauth/github/callback" || query.Get("code_challenge") != wantChallenge || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization URL = %q", got.String())
	}
	if _, exists := query["scope"]; exists {
		t.Fatalf("scope present in %q", got.String())
	}
}

func TestExchangePostsExactValuesAndResolvesIdentityOnce(t *testing.T) {
	tokenCalls, userCalls := 0, 0
	fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			tokenCalls++
			if r.Method != http.MethodPost || r.Header.Get("Accept") != "application/json" {
				t.Errorf("token request = %s accept %q", r.Method, r.Header.Get("Accept"))
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			want := map[string]string{"client_id": "client-id", "client_secret": testSecret, "code": testCode, "redirect_uri": "https://graphnest.example/auth/oauth/github/callback", "code_verifier": "exact-verifier"}
			for key, value := range want {
				if r.Form.Get(key) != value {
					t.Errorf("%s = %q", key, r.Form.Get(key))
				}
			}
			fmt.Fprintf(w, `{"access_token":%q,"token_type":"bEaReR","scope":""}`, testToken)
		case "/api/v3/user":
			userCalls++
			if r.Header.Get("Authorization") != "Bearer "+testToken || r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("User-Agent") != "GraphNest" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
				t.Errorf("user headers = %#v", r.Header)
			}
			fmt.Fprint(w, `{"id":42,"login":"ada","name":"  Ada Lovelace  "}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})
	identity, err := fixture.client.Exchange(t.Context(), testCode, "exact-verifier", "ignored-nonce")
	if err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 1 || userCalls != 1 || identity.Provider != "oauth" || identity.Issuer != fixture.server.URL || identity.Subject != "42" || identity.LinkID != "github:"+fixture.server.URL+":42" || identity.DisplayName != "Ada Lovelace" {
		t.Fatalf("calls = %d/%d, identity = %#v", tokenCalls, userCalls, identity)
	}
}

func TestExchangeNeverFollowsTokenRedirect(t *testing.T) {
	redirected := false
	fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/oauth/access_token" {
			http.Redirect(w, r, "/redirect-canary", http.StatusFound)
			return
		}
		redirected = true
	})
	if _, err := fixture.client.Exchange(t.Context(), testCode, "verifier", ""); err == nil {
		t.Fatal("redirecting token endpoint succeeded")
	}
	if redirected {
		t.Fatal("token redirect was followed")
	}
}

func TestExchangeStatusErrorsExcludeResponseBodiesAndCredentials(t *testing.T) {
	for _, failedPath := range []string{"/login/oauth/access_token", "/api/v3/user"} {
		t.Run(failedPath, func(t *testing.T) {
			fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == failedPath {
					w.WriteHeader(http.StatusBadGateway)
					fmt.Fprint(w, bodyCanary+testToken+testSecret+testCode)
					return
				}
				fmt.Fprint(w, validToken())
			})
			_, err := fixture.client.Exchange(t.Context(), testCode, "verifier", "")
			if err == nil {
				t.Fatal("status error succeeded")
			}
			for _, canary := range []string{testCode, testSecret, testToken, bodyCanary} {
				if strings.Contains(err.Error(), canary) {
					t.Fatalf("error leaked canary: %q", err)
				}
			}
		})
	}
}

func TestExchangeRejectsNonOKGitHubUserStatus(t *testing.T) {
	fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/oauth/access_token" {
			fmt.Fprint(w, validToken())
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":42,"login":"ada"}`)
	})
	if _, err := fixture.client.Exchange(t.Context(), testCode, "verifier", ""); err == nil {
		t.Fatal("GitHub user status 201 succeeded")
	}
}

func TestExchangeRejectsUnsafeResponsesWithoutLeakingCanaries(t *testing.T) {
	tests := []struct {
		name, tokenBody, userBody string
	}{
		{"empty token", `{"access_token":"","token_type":"bearer","scope":""}`, ""},
		{"wrong token type", `{"access_token":"` + testToken + `","token_type":"mac","scope":""}`, ""},
		{"granted scope", `{"access_token":"` + testToken + `","token_type":"bearer","scope":"repo"}`, ""},
		{"trailing token JSON", `{"access_token":"` + testToken + `","token_type":"bearer","scope":""} ` + bodyCanary, ""},
		{"oversized token body", strings.Repeat("x", 64*1024+1) + bodyCanary, ""},
		{"non-positive ID", validToken(), `{"id":0,"login":"ada"}`},
		{"invalid UTF-8 login", validToken(), "{\"id\":42,\"login\":\"\xff" + bodyCanary + "\"}"},
		{"control login", validToken(), `{"id":42,"login":"ada\u000a` + bodyCanary + `"}`},
		{"oversized login", validToken(), `{"id":42,"login":"` + strings.Repeat("a", 257) + bodyCanary + `"}`},
		{"oversized name", validToken(), `{"id":42,"login":"ada","name":"` + strings.Repeat("a", 257) + bodyCanary + `"}`},
		{"trailing user JSON", validToken(), `{"id":42,"login":"ada"} ` + bodyCanary},
		{"oversized user body", validToken(), strings.Repeat("x", 64*1024+1) + bodyCanary},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login/oauth/access_token" {
					fmt.Fprint(w, test.tokenBody)
					return
				}
				fmt.Fprint(w, test.userBody)
			})
			_, err := fixture.client.Exchange(t.Context(), testCode, "verifier", "")
			if err == nil {
				t.Fatal("unsafe response succeeded")
			}
			for _, canary := range []string{testCode, testSecret, testToken, bodyCanary} {
				if strings.Contains(err.Error(), canary) {
					t.Fatalf("error leaked canary: %q", err)
				}
			}
		})
	}
}

func TestExchangeFallsBackToBoundedLogin(t *testing.T) {
	for _, name := range []string{"null", `"   "`} {
		t.Run(name, func(t *testing.T) {
			fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login/oauth/access_token" {
					fmt.Fprint(w, validToken())
					return
				}
				fmt.Fprintf(w, `{"id":42,"login":"ada","name":%s}`, name)
			})
			identity, err := fixture.client.Exchange(t.Context(), testCode, "verifier", "")
			if err != nil || identity.DisplayName != "ada" {
				t.Fatalf("identity = %#v, error = %v", identity, err)
			}
		})
	}
}

func TestExchangeWithAccessSyncCollectsInstallationRepositories(t *testing.T) {
	var paths []string
	fixture := newSyncFixture(t, 532, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		if r.URL.Path != "/login/oauth/access_token" && r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("authorization = %q for %s", r.Header.Get("Authorization"), r.URL.Path)
		}
		switch r.URL.Path {
		case "/login/oauth/access_token":
			fmt.Fprint(w, validToken())
		case "/api/v3/user":
			fmt.Fprint(w, `{"id":42,"login":"ada","name":"Ada"}`)
		case "/api/v3/user/installations":
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `{"total_count":3,"installations":[{"id":12,"app_id":532}]}`)
				return
			}
			w.Header().Set("Link", `</api/v3/user/installations?per_page=100&page=2>; rel="next"`)
			fmt.Fprint(w, `{"total_count":3,"installations":[{"id":10,"app_id":532},{"id":11,"app_id":999}]}`)
		case "/api/v3/user/installations/10/repositories":
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `{"total_count":3,"repositories":[{"id":103}]}`)
				return
			}
			w.Header().Set("Link", `<?per_page=100&page=2>; rel="next"`)
			fmt.Fprint(w, `{"total_count":3,"repositories":[{"id":101},{"id":102}]}`)
		case "/api/v3/user/installations/12/repositories":
			fmt.Fprint(w, `{"total_count":1,"repositories":[{"id":104},{"id":0},{"id":-5}]}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})
	identity, err := fixture.client.Exchange(t.Context(), testCode, "verifier", "")
	if err != nil {
		t.Fatal(err)
	}
	if identity.ProviderToken != testToken {
		t.Fatalf("access-sync identities must carry the provider token for MCP refresh, got %q", identity.ProviderToken)
	}
	if identity.Login != "ada" || identity.AccessSync == nil || fmt.Sprint(identity.AccessSync.RepositoryIDs) != "[101 102 103 104]" {
		t.Fatalf("identity = %#v", identity)
	}
	for _, path := range paths {
		if strings.HasPrefix(path, "/api/v3/user/installations/11/") {
			t.Fatalf("foreign installation was queried: %v", paths)
		}
	}
}

func TestExchangeWithoutAccessSyncNeverListsInstallations(t *testing.T) {
	fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			fmt.Fprint(w, validToken())
		case "/api/v3/user":
			fmt.Fprint(w, `{"id":42,"login":"ada"}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})
	identity, err := fixture.client.Exchange(t.Context(), testCode, "verifier", "")
	if err != nil || identity.AccessSync != nil || identity.Login != "" || identity.ProviderToken != "" {
		t.Fatalf("identity = %#v, error = %v", identity, err)
	}
}

func TestExchangeWithAccessSyncFailsClosed(t *testing.T) {
	tests := []struct {
		name, installations, repositories string
		status                            int
		pages                             int
	}{
		{"installations status", "", "", http.StatusBadGateway, 0},
		{"installations trailing JSON", `{"installations":[]} ` + bodyCanary, "", http.StatusOK, 0},
		{"repositories status", `{"installations":[{"id":10,"app_id":532}]}`, bodyCanary, http.StatusBadGateway, 0},
		{"repositories trailing JSON", `{"installations":[{"id":10,"app_id":532}]}`, `{"repositories":[{"id":1}]} ` + bodyCanary, http.StatusOK, 0},
		{"unbounded pagination", `{"installations":[{"id":10,"app_id":532}]}`, `{"repositories":[{"id":1}]}`, http.StatusOK, maxRepositoryPages + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSyncFixture(t, 532, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/login/oauth/access_token":
					fmt.Fprint(w, validToken())
				case "/api/v3/user":
					fmt.Fprint(w, `{"id":42,"login":"ada"}`)
				case "/api/v3/user/installations":
					if test.installations == "" {
						w.WriteHeader(test.status)
						fmt.Fprint(w, bodyCanary)
						return
					}
					fmt.Fprint(w, test.installations)
				default:
					if test.pages > 0 {
						w.Header().Set("Link", `<https://`+r.Host+r.URL.Path+`?page=next>; rel="next"`)
					}
					w.WriteHeader(test.status)
					fmt.Fprint(w, test.repositories)
				}
			})
			_, err := fixture.client.Exchange(t.Context(), testCode, "verifier", "")
			if err == nil {
				t.Fatal("unsafe access sync succeeded")
			}
			for _, canary := range []string{testCode, testSecret, testToken, bodyCanary} {
				if strings.Contains(err.Error(), canary) {
					t.Fatalf("error leaked canary: %q", err)
				}
			}
		})
	}
}

func TestAccessibleRepositoriesRejectsCumulativeInstallationOverflow(t *testing.T) {
	writeInstallations := func(w http.ResponseWriter, start, count int) {
		fmt.Fprint(w, `{"installations":[`)
		for index := 0; index < count; index++ {
			if index > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"id":%d,"app_id":999}`, start+index)
		}
		fmt.Fprint(w, `]}`)
	}
	pages, repositoryCalls := 0, 0
	fixture := newSyncFixture(t, 532, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/user/installations" {
			repositoryCalls++
			return
		}
		pages++
		if r.URL.Query().Get("page") == "2" {
			writeInstallations(w, 301, 201)
			return
		}
		w.Header().Set("Link", `<https://`+r.Host+`/api/v3/user/installations?per_page=100&page=2>; rel="next"`)
		writeInstallations(w, 1, 300)
	})
	ids, err := fixture.client.AccessibleRepositories(t.Context(), testToken)
	if err == nil || ids != nil || pages != 2 || repositoryCalls != 0 {
		t.Fatalf("ids=%v, err=%v, pages=%d, repository calls=%d", ids, err, pages, repositoryCalls)
	}
}

func TestAccessibleRepositoriesBoundsCyclicEmptyInstallationPages(t *testing.T) {
	pages := 0
	fixture := newSyncFixture(t, 532, func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.Header().Set("Link", `<https://`+r.Host+r.URL.Path+`?page=again>; rel="next"`)
		fmt.Fprint(w, `{"installations":[]}`)
	})
	ids, err := fixture.client.AccessibleRepositories(t.Context(), testToken)
	if err == nil || ids != nil || pages != maxRepositoryPages {
		t.Fatalf("ids=%v, err=%v, pages=%d", ids, err, pages)
	}
}

func TestAccessibleRepositoriesRejectsCrossOriginInstallationPages(t *testing.T) {
	for _, link := range []string{"http://example.invalid/installations", "//example.invalid/installations"} {
		t.Run(link, func(t *testing.T) {
			fixture := newSyncFixture(t, 532, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Link", `<`+link+`>; rel="next"`)
				fmt.Fprint(w, `{"installations":[]}`)
			})
			ids, err := fixture.client.AccessibleRepositories(t.Context(), testToken)
			if err == nil || ids != nil {
				t.Fatalf("ids=%v, err=%v", ids, err)
			}
		})
	}
}

func TestAccessibleRepositoriesFailsOnLaterInstallationPage(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"HTTP error", bodyCanary, http.StatusBadGateway},
		{"JSON error", `{"installations":[}`, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			repositoryCalls := 0
			fixture := newSyncFixture(t, 532, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v3/user/installations":
					if r.URL.Query().Get("page") == "2" {
						w.WriteHeader(test.status)
						fmt.Fprint(w, test.body)
						return
					}
					w.Header().Set("Link", `<https://`+r.Host+r.URL.Path+`?page=2>; rel="next"`)
					fmt.Fprint(w, `{"installations":[{"id":10,"app_id":532}]}`)
				case "/api/v3/user/installations/10/repositories":
					repositoryCalls++
					fmt.Fprint(w, `{"repositories":[{"id":101}]}`)
				default:
					t.Errorf("unexpected request %s", r.URL)
				}
			})
			ids, err := fixture.client.AccessibleRepositories(t.Context(), testToken)
			if err == nil || ids != nil || repositoryCalls != 0 {
				t.Fatalf("ids=%v, err=%v, repository calls=%d", ids, err, repositoryCalls)
			}
			if strings.Contains(err.Error(), test.body) || strings.Contains(err.Error(), testToken) {
				t.Fatalf("error leaked response or token: %v", err)
			}
		})
	}
}

func validToken() string {
	return `{"access_token":"` + testToken + `","token_type":"bearer","scope":""}`
}

type fixture struct {
	server *httptest.Server
	client *Client
}

func newFixture(t *testing.T, handler http.HandlerFunc) fixture {
	t.Helper()
	return newSyncFixture(t, 0, handler)
}

func newSyncFixture(t *testing.T, appID int64, handler http.HandlerFunc) fixture {
	t.Helper()
	if handler == nil {
		handler = func(http.ResponseWriter, *http.Request) {}
	}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	web := mustURL(t, server.URL)
	api := mustURL(t, server.URL+"/api/v3")
	endpoints := githubapp.Endpoints{Web: web, API: api, Upload: web, Git: web}
	clientHTTP, err := githubapp.NewHTTPClient(serverCertificatePEM(t, server), endpoints, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(endpoints, mustURL(t, "https://graphnest.example"), "client-id", []byte(testSecret), "2022-11-28", clientHTTP)
	if err != nil {
		t.Fatal(err)
	}
	client.AccessSyncAppID = appID
	return fixture{server: server, client: client}
}

func endpointsFor(t *testing.T, web, api string) githubapp.Endpoints {
	t.Helper()
	return githubapp.Endpoints{Web: mustURL(t, web), API: mustURL(t, api), Upload: mustURL(t, api), Git: mustURL(t, api)}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	value, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func serverCertificatePEM(t *testing.T, server *httptest.Server) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
}

func TestAccessibleRepositoriesReportsRejectedTokens(t *testing.T) {
	fixture := newSyncFixture(t, 532, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/user/installations" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	})
	_, err := fixture.client.AccessibleRepositories(t.Context(), "gho_stale")
	var rejected TokenRejectedError
	if !errors.As(err, &rejected) || rejected.Status != http.StatusUnauthorized || !rejected.Unauthorized() {
		t.Fatalf("err=%v, want TokenRejectedError", err)
	}
	fixture.client.AccessSyncAppID = 0
	if _, err := fixture.client.AccessibleRepositories(t.Context(), "gho_stale"); err == nil || errors.As(err, &rejected) {
		t.Fatalf("without access sync err=%v, want a plain configuration error", err)
	}
}

func TestAccessibleRepositoriesDistinguishesRateLimitsFromRejectedTokens(t *testing.T) {
	for _, failedPath := range []string{
		"/api/v3/user/installations?per_page=100",
		"/api/v3/user/installations/10/repositories?per_page=100",
		"/api/v3/user/installations/10/repositories?page=2",
	} {
		for _, test := range []struct {
			name, header, value string
			status              int
			rejected            bool
		}{
			{"unauthorized", "", "", http.StatusUnauthorized, true},
			{"forbidden", "", "", http.StatusForbidden, true},
			{"primary rate limit", "X-RateLimit-Remaining", "0", http.StatusForbidden, false},
			{"secondary rate limit", "Retry-After", "60", http.StatusForbidden, false},
			{"too many requests", "", "", http.StatusTooManyRequests, false},
			{"unavailable", "", "", http.StatusBadGateway, false},
		} {
			t.Run(failedPath+"/"+test.name, func(t *testing.T) {
				fixture := newSyncFixture(t, 532, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.RequestURI() == failedPath {
						if test.header != "" {
							w.Header().Set(test.header, test.value)
						}
						w.WriteHeader(test.status)
						fmt.Fprint(w, bodyCanary)
						return
					}
					switch r.URL.Path {
					case "/api/v3/user/installations":
						fmt.Fprint(w, `{"installations":[{"id":10,"app_id":532}]}`)
					case "/api/v3/user/installations/10/repositories":
						w.Header().Set("Link", `<https://`+r.Host+r.URL.Path+`?page=2>; rel="next"`)
						fmt.Fprint(w, `{"repositories":[{"id":101}]}`)
					default:
						t.Errorf("unexpected request %s", r.URL)
					}
				})
				_, err := fixture.client.AccessibleRepositories(t.Context(), testToken)
				var rejected TokenRejectedError
				if err == nil || errors.As(err, &rejected) != test.rejected {
					t.Fatalf("error=%v, want rejected=%v", err, test.rejected)
				}
				if strings.Contains(err.Error(), bodyCanary) || strings.Contains(err.Error(), testToken) {
					t.Fatalf("error leaked response or token: %v", err)
				}
			})
		}
	}
}
