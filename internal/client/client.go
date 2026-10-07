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
	"time"

	"github.com/balcsida/graphnest/internal/httpclient"
	"github.com/balcsida/graphnest/pkg/api"
)

const defaultTimeout = 2 * time.Minute

// Config comes from the environment only; a token never comes from arguments.
type Config struct {
	ServerURL string        // GRAPHNEST_SERVER_URL, required, http or https
	Token     string        // GRAPHNEST_TOKEN, or the trimmed contents of the file named by GRAPHNEST_TOKEN_FILE; exactly one must be set
	CAPEM     []byte        // contents of GRAPHNEST_CA_FILE when set
	Timeout   time.Duration // whole-request timeout; zero means 2 minutes
}

// FromEnv reads Config from environment variables. Errors name variables, never values.
func FromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	config := Config{ServerURL: getenv("GRAPHNEST_SERVER_URL"), Token: getenv("GRAPHNEST_TOKEN")}
	if config.ServerURL == "" {
		return Config{}, errors.New("GRAPHNEST_SERVER_URL is required")
	}
	tokenFile := getenv("GRAPHNEST_TOKEN_FILE")
	switch {
	case config.Token != "" && tokenFile != "":
		return Config{}, errors.New("set only one of GRAPHNEST_TOKEN and GRAPHNEST_TOKEN_FILE")
	case config.Token == "" && tokenFile == "":
		return Config{}, errors.New("GRAPHNEST_TOKEN or GRAPHNEST_TOKEN_FILE is required")
	case tokenFile != "":
		data, err := readFile(tokenFile)
		if err != nil {
			return Config{}, errors.New("GRAPHNEST_TOKEN_FILE cannot be read")
		}
		if config.Token = strings.TrimSpace(string(data)); config.Token == "" {
			return Config{}, errors.New("GRAPHNEST_TOKEN_FILE is empty")
		}
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

// Client talks to a GraphNest server with a bearer token.
type Client struct {
	http  *http.Client
	base  string
	token string
}

// New validates config and builds a client that refuses cross-origin redirects.
func New(config Config) (*Client, error) {
	u, err := url.Parse(config.ServerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("GRAPHNEST_SERVER_URL must be an http or https URL")
	}
	if config.Token == "" {
		return nil, errors.New("token is required")
	}
	httpClient, err := httpclient.New(config.CAPEM)
	if err != nil {
		return nil, err
	}
	httpClient.Timeout = config.Timeout
	if httpClient.Timeout == 0 {
		httpClient.Timeout = defaultTimeout
	}
	return &Client{http: httpClient, base: strings.TrimRight(config.ServerURL, "/"), token: config.Token}, nil
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
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return errors.New("build request failed")
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // drop the URL from the message
		}
		return fmt.Errorf("%s %s: %w", method, path, err)
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
