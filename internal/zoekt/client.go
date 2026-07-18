package zoekt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/pkg/api"
)

var (
	ErrUnavailable      = errors.New("zoekt unavailable")
	ErrInvalidQuery     = errors.New("zoekt invalid query")
	ErrResponseTooLarge = errors.New("zoekt response too large")
)

const maxResponseBytes int64 = 256 << 10

type Client struct {
	endpoint string
	http     *http.Client
	maxBytes int64
	metrics  *observability.Metrics
}

func New(baseURL string, client *http.Client, maxBytes int64, metrics *observability.Metrics) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("%w: invalid URL", ErrUnavailable)
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("%w: response limit must be positive", ErrResponseTooLarge)
	}
	if maxBytes > maxResponseBytes {
		maxBytes = maxResponseBytes
	}
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/search"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	if metrics == nil {
		metrics = observability.New()
	}
	return &Client{endpoint: parsed.String(), http: &copy, maxBytes: maxBytes, metrics: metrics}, nil
}

func (client *Client) Search(ctx context.Context, request search.BackendRequest) (api.SearchResponse, error) {
	if strings.TrimSpace(request.Query) == "" {
		return api.SearchResponse{}, ErrInvalidQuery
	}
	if request.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, request.Timeout)
		defer cancel()
	}
	result, err := client.call(ctx, wireRequest{
		Q:       request.Query,
		RepoIDs: append([]uint32{}, request.RepositoryIDs...),
		Opts: wireOptions{
			NumContextLines:    request.ContextLines,
			MaxDocDisplayCount: request.Limit,
			MaxWallTime:        int64(request.Timeout),
		},
	}, client.maxBytes)
	if err != nil {
		return api.SearchResponse{}, err
	}
	return normalize(result.Files, client.maxBytes), nil
}

func (client *Client) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err := client.call(ctx, wireRequest{RepoIDs: []uint32{}, Opts: wireOptions{MaxDocDisplayCount: 1, MaxWallTime: int64(time.Second)}}, client.maxBytes)
	return err
}

func (client *Client) call(ctx context.Context, payload wireRequest, maxBytes int64) (result wireResult, err error) {
	if maxBytes > maxResponseBytes {
		maxBytes = maxResponseBytes
	}
	if payload.RepoIDs == nil {
		payload.RepoIDs = []uint32{}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return wireResult{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	started := time.Now()
	defer func() { client.metrics.ObserveBackend(time.Since(started), err) }()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return wireResult{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return wireResult{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusBadRequest {
		return wireResult{}, ErrInvalidQuery
	}
	if response.StatusCode != http.StatusOK {
		return wireResult{}, fmt.Errorf("%w: HTTP %d", ErrUnavailable, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return wireResult{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if int64(len(data)) > maxBytes {
		return wireResult{}, ErrResponseTooLarge
	}
	var envelope wireResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&envelope); err != nil {
		return wireResult{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return wireResult{}, fmt.Errorf("%w: trailing JSON", ErrUnavailable)
	}
	if envelope.Error != "" {
		return wireResult{}, fmt.Errorf("%w: %s", ErrInvalidQuery, envelope.Error)
	}
	return envelope.Result, nil
}

func normalize(files []wireFile, maxBytes int64) api.SearchResponse {
	if maxBytes > maxResponseBytes {
		maxBytes = maxResponseBytes
	}
	response := api.SearchResponse{}
	remaining := maxBytes
	for _, file := range files {
		for _, line := range file.LineMatches {
			preview := trimUTF8(line.Line, &remaining)
			response.Matches = append(response.Matches, api.SearchMatch{Path: path.Clean(file.FileName), SHA: file.Version, LineNumber: line.LineNumber, LineStart: line.LineStart, LineEnd: line.LineEnd, Preview: string(preview), Score: line.Score, ZoektID: file.RepositoryID})
		}
	}
	return response
}

func trimUTF8(value []byte, remaining *int64) []byte {
	if int64(len(value)) <= *remaining {
		*remaining -= int64(len(value))
		return value
	}
	limit := int(*remaining)
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	*remaining = 0
	return value[:limit]
}

type wireRequest struct {
	Q       string      `json:"Q"`
	RepoIDs []uint32    `json:"RepoIDs"`
	Opts    wireOptions `json:"Opts"`
}

type wireOptions struct {
	NumContextLines    int   `json:"NumContextLines"`
	MaxDocDisplayCount int   `json:"MaxDocDisplayCount"`
	MaxWallTime        int64 `json:"MaxWallTime"`
}

type wireResponse struct {
	Result wireResult `json:"Result"`
	Error  string     `json:"Error"`
}

type wireResult struct {
	Files []wireFile `json:"Files"`
}

type wireFile struct {
	FileName     string      `json:"FileName"`
	Repository   string      `json:"Repository"`
	Version      string      `json:"Version"`
	Branches     []string    `json:"Branches"`
	RepositoryID uint32      `json:"RepositoryID"`
	Score        float64     `json:"Score"`
	LineMatches  []wireMatch `json:"LineMatches"`
}

type wireMatch struct {
	Line       []byte  `json:"Line"`
	LineNumber int     `json:"LineNumber"`
	LineStart  int     `json:"LineStart"`
	LineEnd    int     `json:"LineEnd"`
	Score      float64 `json:"Score"`
}
