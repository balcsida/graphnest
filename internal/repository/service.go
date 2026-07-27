package repository

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/pkg/api"
)

const (
	defaultMaxFileBytes = int64(1 << 20)
	defaultMaxLines     = 1000
	githubEnvelopeBytes = int64(64 << 10)
)

var (
	ErrNotIndexed            = errors.New("not_indexed")
	ErrInvalidPath           = errors.New("invalid_path")
	ErrInvalidRange          = errors.New("invalid_range")
	ErrInvalidFile           = errors.New("invalid_file")
	ErrFileTooLarge          = errors.New("file_too_large")
	ErrBinaryFile            = errors.New("binary_file")
	ErrLineOutOfRange        = errors.New("line_out_of_range")
	ErrSearchNodeUnavailable = errors.New("search_node_unavailable")
)

type ServiceStore interface {
	AuthorizedRepositories(context.Context, int64, []int64, []string) ([]Repository, error)
	AuthorizedRepository(context.Context, int64, []int64, int64) (Repository, error)
	AllAuthorizedRepositories(context.Context, []string) ([]Repository, error)
	AnyAuthorizedRepository(context.Context, int64) (Repository, error)
}

type ContentReader interface {
	ReadContents(context.Context, int64, string, string, string, string, int64) (githubapp.Content, error)
}

type Service struct {
	Store        ServiceStore
	GitHub       ContentReader
	MaxFileBytes int64
	MaxLines     int
}

func (service *Service) List(ctx context.Context, principal authn.Principal) ([]api.RepositorySummary, error) {
	repositories, err := service.authorizedRepositories(ctx, principal)
	if err != nil {
		return nil, err
	}
	summaries := make([]api.RepositorySummary, len(repositories))
	for index, repository := range repositories {
		summaries[index], err = summarize(repository)
		if err != nil {
			return nil, err
		}
	}
	return summaries, nil
}

func (service *Service) Status(ctx context.Context, principal authn.Principal, repositoryID int64) (api.RepositorySummary, error) {
	repository, err := service.authorizedRepository(ctx, principal, repositoryID)
	if err != nil {
		return api.RepositorySummary{}, err
	}
	return summarize(repository)
}

func (service *Service) ReadFile(ctx context.Context, principal authn.Principal, request api.ReadFileRequest) (api.ReadFileResponse, error) {
	repository, err := service.authorizedRepository(ctx, principal, request.RepositoryID)
	if err != nil {
		return api.ReadFileResponse{}, err
	}
	if repository.IndexedSHA == "" {
		return api.ReadFileResponse{}, ErrNotIndexed
	}
	if !validPath(request.Path) {
		return api.ReadFileResponse{}, ErrInvalidPath
	}
	start := request.StartLine
	if start == 0 {
		start = 1
	}
	if start < 1 || request.EndLine < 0 || request.EndLine != 0 && request.EndLine < start {
		return api.ReadFileResponse{}, ErrInvalidRange
	}
	owner, name, ok := strings.Cut(repository.Name, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return api.ReadFileResponse{}, ErrInvalidFile
	}
	maxBytes := service.maxFileBytes()
	wireBytes := int64(base64.StdEncoding.EncodedLen(int(maxBytes))) + githubEnvelopeBytes
	content, err := service.GitHub.ReadContents(ctx, repository.InstallationID, owner, name, request.Path, repository.IndexedSHA, wireBytes)
	if err != nil {
		return api.ReadFileResponse{}, err
	}
	data, err := decodeContent(content, maxBytes)
	if err != nil {
		return api.ReadFileResponse{}, err
	}
	lines := bytes.Split(data, []byte{'\n'})
	if start > len(lines) {
		return api.ReadFileResponse{}, ErrLineOutOfRange
	}
	end := request.EndLine
	if end == 0 || end > len(lines) {
		end = len(lines)
	}
	truncated := end-start+1 > service.maxLines()
	if truncated {
		end = start + service.maxLines() - 1
	}
	current, err := service.authorizedRepository(ctx, principal, request.RepositoryID)
	if err != nil {
		return api.ReadFileResponse{}, err
	}
	if current.ID != repository.ID || current.IndexedSHA == "" || current.IndexedSHA != repository.IndexedSHA {
		return api.ReadFileResponse{}, ErrNotIndexed
	}
	return api.ReadFileResponse{
		RepositoryID: request.RepositoryID, Path: request.Path, IndexedSHA: repository.IndexedSHA, BlobSHA: content.SHA,
		Content: string(bytes.Join(lines[start-1:end], []byte{'\n'})), StartLine: start, EndLine: end, Truncated: truncated,
	}, nil
}

func (service *Service) authorizedRepositories(ctx context.Context, principal authn.Principal) ([]Repository, error) {
	if principal.Administrator {
		return service.Store.AllAuthorizedRepositories(ctx, principal.RepositoryNames)
	}
	return service.Store.AuthorizedRepositories(ctx, principal.InstallationID, principal.RepositoryIDs, principal.RepositoryNames)
}

func (service *Service) authorizedRepository(ctx context.Context, principal authn.Principal, repositoryID int64) (Repository, error) {
	if principal.Administrator {
		return service.Store.AnyAuthorizedRepository(ctx, repositoryID)
	}
	return service.Store.AuthorizedRepository(ctx, principal.InstallationID, principal.RepositoryIDs, repositoryID)
}

func summarize(repository Repository) (api.RepositorySummary, error) {
	if repository.SearchNode == "" {
		return api.RepositorySummary{}, ErrSearchNodeUnavailable
	}
	return api.RepositorySummary{
		ID: repository.GitHubID, GitHubID: repository.GitHubID, Name: repository.Name, Branch: repository.Branch,
		DesiredSHA: repository.DesiredSHA, IndexedSHA: repository.IndexedSHA, Status: repository.Status,
		ErrorCode: repository.ErrorCode, SearchNode: repository.SearchNode, LastIndexedAt: repository.LastIndexedAt,
	}, nil
}

func validPath(value string) bool {
	return value != "" && value != "." && !path.IsAbs(value) && path.Clean(value) == value &&
		value != ".." && !strings.HasPrefix(value, "../") && !strings.ContainsAny(value, "\\\x00")
}

func decodeContent(content githubapp.Content, maxBytes int64) ([]byte, error) {
	if content.Type != "file" || content.Encoding != "base64" || content.Size < 0 || content.SHA == "" {
		return nil, ErrInvalidFile
	}
	if content.Size > maxBytes {
		return nil, ErrFileTooLarge
	}
	encoded := io.LimitReader(strings.NewReader(content.Content), int64(len(content.Content)))
	data, err := io.ReadAll(io.LimitReader(base64.NewDecoder(base64.StdEncoding, encoded), maxBytes+1))
	if err != nil {
		return nil, ErrInvalidFile
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrFileTooLarge
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return nil, ErrBinaryFile
	}
	return data, nil
}

func (service *Service) maxFileBytes() int64 {
	if service.MaxFileBytes <= 0 || service.MaxFileBytes > defaultMaxFileBytes {
		return defaultMaxFileBytes
	}
	return service.MaxFileBytes
}

func (service *Service) maxLines() int {
	if service.MaxLines <= 0 {
		return defaultMaxLines
	}
	return service.MaxLines
}
