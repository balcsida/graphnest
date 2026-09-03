package githuboauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/githubapp"
	"golang.org/x/oauth2"
)

const (
	maxResponseBytes = 64 * 1024
	// maxRepositoryPages bounds the accessible-repository walk per installation;
	// GitHub serves at most 100 repositories per page.
	maxRepositoryPages = 50
	maxInstallations   = 500
)

type Client struct {
	endpoints  githubapp.Endpoints
	oauth      oauth2.Config
	http       *http.Client
	issuer     string
	apiVersion string
	// AccessSyncAppID enables GitHub-derived access. The OAuth client must then
	// be the GitHub App's own OAuth credential: after identity lookup the
	// user-to-server token lists the user's installations of that App and the
	// repositories the user can access through each, which become the user's
	// provider-derived grants. Zero disables the sync.
	AccessSyncAppID int64
}

func NewClient(endpoints githubapp.Endpoints, publicURL *url.URL, clientID string, clientSecret []byte, apiVersion string, httpClient *http.Client) (*Client, error) {
	issuer, err := canonicalIssuer(endpoints.Web)
	if err != nil {
		return nil, err
	}
	if endpoints.API == nil || publicURL == nil || httpClient == nil {
		return nil, errors.New("GitHub OAuth configuration is invalid")
	}
	callback := publicURL.ResolveReference(&url.URL{Path: "/auth/oauth/github/callback"}).String()
	redirectDenyingClient := *httpClient
	redirectDenyingClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		endpoints: endpoints,
		oauth: oauth2.Config{
			ClientID: clientID, ClientSecret: string(clientSecret), RedirectURL: callback,
			Endpoint: oauth2.Endpoint{
				AuthURL:  githubapp.EndpointURL(endpoints.Web, "login", "oauth", "authorize"),
				TokenURL: githubapp.EndpointURL(endpoints.Web, "login", "oauth", "access_token"),
			},
		},
		http: &redirectDenyingClient, issuer: issuer, apiVersion: apiVersion,
	}, nil
}

func (client *Client) AuthorizationURL(state, _ string, verifier string) string {
	return client.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
}

func (client *Client) Exchange(ctx context.Context, code, verifier, _ string) (authn.Identity, error) {
	values := url.Values{
		"client_id":     {client.oauth.ClientID},
		"client_secret": {client.oauth.ClientSecret},
		"code":          {code},
		"redirect_uri":  {client.oauth.RedirectURL},
		"code_verifier": {verifier},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.oauth.Endpoint.TokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return authn.Identity{}, errors.New("build GitHub OAuth token request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.http.Do(request)
	if err != nil {
		return authn.Identity{}, errors.New("GitHub OAuth token request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return authn.Identity{}, fmt.Errorf("GitHub OAuth token status %d", response.StatusCode)
	}
	data, err := boundedBody(response.Body)
	if err != nil {
		return authn.Identity{}, errors.New("read GitHub OAuth token response")
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	if err := decodeOne(data, &token); err != nil || token.AccessToken == "" || !strings.EqualFold(token.TokenType, "bearer") || token.Scope != "" {
		return authn.Identity{}, errors.New("GitHub OAuth token response is invalid")
	}

	request, err = http.NewRequestWithContext(ctx, http.MethodGet, githubapp.EndpointURL(client.endpoints.API, "user"), nil)
	if err != nil {
		return authn.Identity{}, errors.New("build GitHub user request")
	}
	githubapp.SetAPIHeaders(request.Header, client.apiVersion)
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err = client.http.Do(request)
	if err != nil {
		return authn.Identity{}, errors.New("GitHub user request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return authn.Identity{}, fmt.Errorf("GitHub user status %d", response.StatusCode)
	}
	data, err = boundedBody(response.Body)
	if err != nil {
		return authn.Identity{}, errors.New("read GitHub user response")
	}
	var user struct {
		ID    int64   `json:"id"`
		Login string  `json:"login"`
		Name  *string `json:"name"`
	}
	if err := decodeOne(data, &user); err != nil || user.ID <= 0 || !validName(user.Login) || (user.Name != nil && !validOptionalName(*user.Name)) {
		return authn.Identity{}, errors.New("GitHub user response is invalid")
	}
	displayName := user.Login
	if user.Name != nil && strings.TrimSpace(*user.Name) != "" {
		displayName = strings.TrimSpace(*user.Name)
	}
	subject := strconv.FormatInt(user.ID, 10)
	linkID := "github:" + client.issuer + ":" + subject
	if len(client.issuer) > 2048 || len(linkID) > 2048 {
		return authn.Identity{}, errors.New("GitHub user identity is invalid")
	}
	identity := authn.Identity{Provider: authn.ProviderOAuth, Issuer: client.issuer, Subject: subject, LinkID: linkID, DisplayName: displayName}
	if client.AccessSyncAppID > 0 {
		repositories, err := client.accessibleRepositories(ctx, token.AccessToken)
		if err != nil {
			return authn.Identity{}, err
		}
		identity.Login = user.Login
		identity.AccessSync = &authn.AccessSync{RepositoryIDs: repositories}
	}
	return identity, nil
}

// accessibleRepositories returns the sorted, de-duplicated GitHub repository
// IDs the user can access through installations of the configured App. Any
// failure denies the login: a partial list would silently narrow access while
// an unchecked one could widen it.
func (client *Client) accessibleRepositories(ctx context.Context, accessToken string) ([]int64, error) {
	var installations struct {
		Installations []struct {
			ID    int64 `json:"id"`
			AppID int64 `json:"app_id"`
		} `json:"installations"`
	}
	if _, err := client.getJSON(ctx, githubapp.EndpointURL(client.endpoints.API, "user", "installations")+"?per_page=100", accessToken, &installations); err != nil {
		return nil, fmt.Errorf("GitHub user installations: %w", err)
	}
	if len(installations.Installations) > maxInstallations {
		return nil, errors.New("GitHub user installations exceed the supported bound")
	}
	seen := map[int64]struct{}{}
	for _, installation := range installations.Installations {
		if installation.AppID != client.AccessSyncAppID || installation.ID <= 0 {
			continue
		}
		next := githubapp.EndpointURL(client.endpoints.API, "user", "installations", strconv.FormatInt(installation.ID, 10), "repositories") + "?per_page=100"
		for page := 0; next != ""; page++ {
			if page >= maxRepositoryPages {
				return nil, errors.New("GitHub installation repositories exceed the supported bound")
			}
			var body struct {
				Repositories []struct {
					ID int64 `json:"id"`
				} `json:"repositories"`
			}
			header, err := client.getJSON(ctx, next, accessToken, &body)
			if err != nil {
				return nil, fmt.Errorf("GitHub installation repositories: %w", err)
			}
			for _, repository := range body.Repositories {
				if repository.ID > 0 {
					seen[repository.ID] = struct{}{}
				}
			}
			next = nextPage(header.Get("Link"))
			if next != "" && !client.sameAPIOrigin(next) {
				return nil, errors.New("GitHub pagination left the API origin")
			}
		}
	}
	ids := make([]int64, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

// sameAPIOrigin keeps server-supplied pagination links, and therefore the
// bearer token, on the configured GitHub API origin.
func (client *Client) sameAPIOrigin(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && parsed.Scheme == client.endpoints.API.Scheme && parsed.Host == client.endpoints.API.Host
}

func (client *Client) getJSON(ctx context.Context, rawURL, accessToken string, target any) (http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.New("build request")
	}
	githubapp.SetAPIHeaders(request.Header, client.apiVersion)
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := client.http.Do(request)
	if err != nil {
		return nil, errors.New("request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", response.StatusCode)
	}
	data, err := boundedBody(response.Body)
	if err != nil {
		return nil, errors.New("read response")
	}
	if err := decodeOne(data, target); err != nil {
		return nil, errors.New("response is invalid")
	}
	return response.Header, nil
}

// nextPage extracts the rel="next" target from a GitHub Link header.
func nextPage(link string) string {
	for _, part := range strings.Split(link, ",") {
		segments := strings.Split(strings.TrimSpace(part), ";")
		if len(segments) < 2 || !strings.HasPrefix(segments[0], "<") || !strings.HasSuffix(segments[0], ">") {
			continue
		}
		for _, parameter := range segments[1:] {
			if strings.TrimSpace(parameter) == `rel="next"` {
				return strings.TrimSuffix(strings.TrimPrefix(segments[0], "<"), ">")
			}
		}
	}
	return ""
}

func canonicalIssuer(web *url.URL) (string, error) {
	if web == nil || web.Scheme != "https" || web.Hostname() == "" || web.User != nil || web.RawQuery != "" || web.Fragment != "" || (web.EscapedPath() != "" && web.EscapedPath() != "/") {
		return "", errors.New("GitHub web endpoint must be an HTTPS origin")
	}
	host := strings.ToLower(web.Hostname())
	if port := web.Port(); port != "" && port != "443" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return (&url.URL{Scheme: "https", Host: host}).String(), nil
}

func boundedBody(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes || !utf8.Valid(data) {
		return nil, errors.New("invalid bounded response")
	}
	return data, nil
}

func decodeOne(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func validName(value string) bool {
	return value != "" && validOptionalName(value)
}

func validOptionalName(value string) bool {
	if len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
