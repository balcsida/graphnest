// Package client is the REST client of the graphnest command-line tool.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/balcsida/graphnest/pkg/api"
)

const defaultTimeout = 2 * time.Minute

// Config comes from the environment and the stored login only; a token never comes from arguments.
type Config struct {
	ServerURL string        // GRAPHNEST_SERVER_URL, required, http or https
	Token     string        // GRAPHNEST_TOKEN, or the trimmed contents of the file named by GRAPHNEST_TOKEN_FILE; at most one may be set; without either, Login supplies the credential
	CAPEM     []byte        // contents of GRAPHNEST_CA_FILE when set
	Timeout   time.Duration // whole-request timeout; zero means 2 minutes
	Login     *Login        // stored OAuth login used instead of Token
	Logins    Logins        // where a refreshed Login is saved
	Source    string        // where the credential came from: "GRAPHNEST_TOKEN", "GRAPHNEST_TOKEN_FILE" or "stored login"
}

// ServerFromEnv reads the server URL and optional CA file. Errors name variables, never values.
func ServerFromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	config := Config{ServerURL: getenv("GRAPHNEST_SERVER_URL")}
	if config.ServerURL == "" {
		return Config{}, errors.New("GRAPHNEST_SERVER_URL is required")
	}
	if caFile := getenv("GRAPHNEST_CA_FILE"); caFile != "" {
		data, err := readFile(caFile)
		if err != nil {
			return Config{}, errors.New("GRAPHNEST_CA_FILE cannot be read")
		}
		config.CAPEM = data
	}
	return config, nil
}

// FromEnv reads Config from environment variables, falling back to the stored login for the server.
// Errors name variables, never values.
func FromEnv(getenv func(string) string, readFile func(string) ([]byte, error), logins Logins) (Config, error) {
	config, err := ServerFromEnv(getenv, readFile)
	if err != nil {
		return Config{}, err
	}
	config.Logins = logins
	config.Token = getenv("GRAPHNEST_TOKEN")
	tokenFile := getenv("GRAPHNEST_TOKEN_FILE")
	switch {
	case config.Token != "" && tokenFile != "":
		return Config{}, errors.New("set only one of GRAPHNEST_TOKEN and GRAPHNEST_TOKEN_FILE")
	case config.Token != "":
		config.Source = "GRAPHNEST_TOKEN"
	case tokenFile != "":
		data, err := readFile(tokenFile)
		if err != nil {
			return Config{}, errors.New("GRAPHNEST_TOKEN_FILE cannot be read")
		}
		if config.Token = strings.TrimSpace(string(data)); config.Token == "" {
			return Config{}, errors.New("GRAPHNEST_TOKEN_FILE is empty")
		}
		config.Source = "GRAPHNEST_TOKEN_FILE"
	default:
		const missing = "GRAPHNEST_TOKEN or GRAPHNEST_TOKEN_FILE is required, or run graphnest login"
		origin, err := Origin(config.ServerURL)
		if err != nil {
			return Config{}, errors.New(missing)
		}
		login, ok, err := logins.Load(origin)
		if err != nil {
			return Config{}, fmt.Errorf("the stored login for %s cannot be read: %w; run graphnest login", origin, err)
		}
		if !ok {
			return Config{}, errors.New(missing)
		}
		config.Login, config.Source = &login, "stored login"
	}
	return config, nil
}

// Client talks to a GraphNest server with a bearer token.
type Client struct {
	http   *http.Client
	base   string
	bearer func(context.Context) (string, error)
	stored bool // bearer reads a stored login, so a 401 may mean another process rotated it
}

// New validates config and builds a client that refuses cross-origin redirects.
func New(config Config) (*Client, error) {
	u, err := url.Parse(config.ServerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("GRAPHNEST_SERVER_URL must be an http or https URL")
	}
	if config.Token == "" && config.Login == nil {
		return nil, errors.New("token is required")
	}
	httpClient, err := newHTTPClient(config)
	if err != nil {
		return nil, err
	}
	c := &Client{http: httpClient, base: strings.TrimRight(config.ServerURL, "/")}
	if config.Login != nil {
		source := &loginSource{http: httpClient, logins: config.Logins, login: *config.Login}
		c.bearer, c.stored = source.token, true
	} else {
		token := config.Token
		c.bearer = func(context.Context) (string, error) { return token, nil }
	}
	return c, nil
}

// loginSource hands out the access token of a stored login, refreshing it before it runs short.
type loginSource struct {
	mu     sync.Mutex
	http   *http.Client
	logins Logins
	login  Login
}

// fresh reports whether login outlives a request (the HTTP timeout) plus 30 seconds.
func (s *loginSource) fresh(login Login) bool {
	return time.Until(login.ExpiresAt) >= s.http.Timeout+30*time.Second
}

func (s *loginSource) token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Another graphnest process may have rotated the tokens; never send a refresh token it superseded.
	if stored, ok, loadErr := s.logins.Load(s.login.Server); loadErr == nil && ok && stored.RefreshToken != s.login.RefreshToken {
		s.login = stored
	}
	if s.fresh(s.login) {
		return s.login.AccessToken, nil
	}
	next, err := s.refresh(ctx, s.login)
	if isInvalidGrant(err) {
		// Another graphnest process may have rotated the tokens already.
		stored, ok, loadErr := s.logins.Load(s.login.Server)
		if loadErr != nil {
			return "", loadErr
		}
		if !ok || stored.RefreshToken == s.login.RefreshToken {
			return "", s.expired()
		}
		s.login = stored
		if s.fresh(stored) {
			return stored.AccessToken, nil
		}
		next, err = s.refresh(ctx, stored)
		if isInvalidGrant(err) {
			return "", s.expired()
		}
	}
	if err != nil {
		return "", err
	}
	if err = s.logins.Save(next); err != nil {
		return "", err
	}
	s.login = next
	return next.AccessToken, nil
}

func (s *loginSource) expired() error {
	return fmt.Errorf("the stored login for %s has expired or was revoked; run graphnest login", s.login.Server)
}

func (s *loginSource) refresh(ctx context.Context, login Login) (Login, error) {
	tokens, err := postTokenForm(ctx, s.http, login.TokenEndpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {login.RefreshToken},
		"client_id":     {login.ClientID},
	})
	if err != nil {
		return Login{}, err
	}
	login.AccessToken, login.RefreshToken, login.ExpiresAt = tokens.AccessToken, tokens.RefreshToken, tokens.expiresAt
	return login, nil
}

// Error is the server's error envelope for a non-2xx response.
type Error struct {
	Status    int
	Code      string
	Message   string
	Retryable bool
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("server returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("server returned HTTP %d: %s: %s", e.Status, e.Code, e.Message)
}

// Repository fetches GET /v1/repositories/{id}.
func (c *Client) Repository(ctx context.Context, id int64) (api.RepositorySummary, error) {
	var out api.RepositorySummary
	err := c.do(ctx, http.MethodGet, "/v1/repositories/"+strconv.FormatInt(id, 10), "", "", nil, &out)
	return out, err
}

// GraphStatus fetches GET /v1/graph/repositories/{id}/status.
func (c *Client) GraphStatus(ctx context.Context, id int64) (api.GraphStatus, error) {
	var out api.GraphStatus
	err := c.do(ctx, http.MethodGet, "/v1/graph/repositories/"+strconv.FormatInt(id, 10)+"/status", "", "", nil, &out)
	return out, err
}

// Publish uploads a v2 graph artifact.
func (c *Client) Publish(ctx context.Context, id int64, commit string, expectedGeneration int64, replaceProducer bool, artifact []byte) (api.GraphPublicationResult, error) {
	query := url.Values{
		"repository_id":       {strconv.FormatInt(id, 10)},
		"commit":              {commit},
		"expected_generation": {strconv.FormatInt(expectedGeneration, 10)},
	}
	if replaceProducer {
		query.Set("replace_producer", "true")
	}
	var out api.GraphPublicationResult
	err := c.do(ctx, http.MethodPost, "/v1/graph/uploads", query.Encode(), api.GraphArtifactV2ContentType, artifact, &out)
	return out, err
}

func (c *Client) do(ctx context.Context, method, path, rawQuery, contentType string, body []byte, out any) error {
	target := c.base + path
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	token, err := c.bearer(ctx)
	if err != nil {
		return err
	}
	response, err := c.send(ctx, method, path, target, contentType, token, body)
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusUnauthorized && c.stored {
		// Another graphnest process may have rotated the login, which invalidates the token just used.
		if again, tokenErr := c.bearer(ctx); tokenErr == nil && again != token {
			response.Body.Close()
			if response, err = c.send(ctx, method, path, target, contentType, again, body); err != nil {
				return err
			}
		}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		var envelope struct {
			Error *struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				Retryable bool   `json:"retryable"`
			} `json:"error"`
		}
		if json.NewDecoder(response.Body).Decode(&envelope) != nil || envelope.Error == nil || envelope.Error.Code == "" {
			return &Error{Status: response.StatusCode}
		}
		return &Error{Status: response.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message, Retryable: envelope.Error.Retryable}
	}
	if err = json.NewDecoder(response.Body).Decode(out); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s %s: invalid response: %w", method, path, err)
	}
	return nil
}

// send performs one authenticated request; errors never contain the URL or the token.
func (c *Client) send(ctx context.Context, method, path, target, contentType, token string, body []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("build request failed")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // drop the URL from the message
		}
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return response, nil
}
