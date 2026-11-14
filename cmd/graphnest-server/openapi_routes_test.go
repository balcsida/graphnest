package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/admin"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/authz"
	"github.com/balcsida/graphnest/internal/config"
	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/graphingest"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/mcpserver"
	"github.com/balcsida/graphnest/internal/oauthas"
	"github.com/balcsida/graphnest/internal/observability"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/repository"
	scimapi "github.com/balcsida/graphnest/internal/scim"
	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/balcsida/graphnest/internal/search"
	"github.com/balcsida/graphnest/internal/sso"
	"github.com/balcsida/graphnest/internal/sso/githuboauth"
	"github.com/balcsida/graphnest/internal/sso/oidc"
	"github.com/balcsida/graphnest/internal/supplychain"
	"github.com/balcsida/graphnest/internal/supplychain/review"
	"github.com/balcsida/graphnest/internal/webhook"
	"gopkg.in/yaml.v3"
)

var pathParameter = regexp.MustCompile(`\{[^}]+\}`)

type noopProcessor struct{}

func (noopProcessor) Process(context.Context, webhook.Delivery) (bool, error) { return true, nil }

// TestOpenAPIPathsAreServed fails when docs/openapi.yaml documents a route the
// server does not serve, or marks a served route x-graphnest-status: planned.
//
// shortcut: without credentials a prefix handler answers 401 for any sub-path, so a documented path under a served prefix passes even when that exact path is not routed; authenticate the probe when that gap matters.
func TestOpenAPIPathsAreServed(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}

	store := &postgres.Store{}
	settings := config.Config{Limits: config.Limits{MaxRequestBytes: 1024, MaxResponseBytes: 4096, MaxResults: 10, SCIPMaxUploadBytes: 1024, GraphMaxUploadBytes: 1024}}
	requestAuth := authn.RequestAuthenticator{Bearer: authn.TokenManager{Store: store}, PublicOrigin: "https://graphnest.example"}
	provisioning, err := authn.NewProvisioningAuthenticator([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	providers := []sso.Provider{oidc.NewProvider(nil, nil, nil, nil, 0), githuboauth.NewProvider(nil, nil, nil, nil, 0)}
	supplyChain := func(mux *http.ServeMux) {
		httpapi.RegisterSupplyChain(mux, requestAuth, &supplychain.Service{}, 10, 4096)
		httpapi.RegisterSupplyChainPortfolio(mux, requestAuth, &supplychain.Portfolio{}, 10, 4096, &httpapi.DerivedExport{Service: &supplychain.Service{}})
		httpapi.RegisterSupplyChainImports(mux, requestAuth, &supplychain.Importer{}, &httpapi.UploadGrants{}, 1024, 4096)
		httpapi.RegisterSupplyChainReview(mux, requestAuth, &review.Service{Authorizer: authz.NewPostgres(store)}, 1024, 4096)
	}
	app := newAPIHandlerWithMCP(settings, observability.New(), requestAuth, &search.Service{}, &repository.Service{GitHub: &githubapp.Client{}},
		&scipgraph.Service{}, &graphingest.Service{Store: store}, &graphservice.Service{}, nil, noopProcessor{}, &admin.Service{}, nil, providers, nil,
		&provisioning, &scimapi.Service{BaseURL: "https://graphnest.example", MaxResults: 10}, &oauthas.Server{}, mcpserver.SupplyChainServices{}, supplyChain)
	// Local auth wraps the application handler, as in newDurableRuntime.
	outer := http.NewServeMux()
	httpapi.RegisterLocalAuth(outer, requestAuth.PublicOrigin, &authn.LocalAuthenticator{}, store)
	outer.Handle("/", app)

	for path, item := range spec.Paths {
		status, _ := item["x-graphnest-status"].(string)
		if _, marked := item["x-graphnest-status"]; marked && status != "planned" && status != "preview" {
			t.Errorf("%s: x-graphnest-status %v, want planned or preview", path, item["x-graphnest-status"])
		}
		for _, method := range []string{"get", "post", "put", "patch", "delete"} {
			if _, ok := item[method]; !ok {
				continue
			}
			url := pathParameter.ReplaceAllString(path, "1")
			response := httptest.NewRecorder()
			outer.ServeHTTP(response, httptest.NewRequest(strings.ToUpper(method), url, nil))
			// Method checks run before authentication, so 405 also means the operation is not served.
			switch {
			case status == "planned" && response.Code != http.StatusNotFound:
				t.Errorf("%s %s: status %d, served now: remove x-graphnest-status", strings.ToUpper(method), path, response.Code)
			case status != "planned" && (response.Code == http.StatusNotFound || response.Code == http.StatusMethodNotAllowed):
				t.Errorf("%s %s: documented but not served (status %d)", strings.ToUpper(method), path, response.Code)
			}
		}
	}
}
