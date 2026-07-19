package repository

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"
)

var ErrInvalid = errors.New("invalid repository registry")

type Repository struct {
	ID             int64      `json:"id"`
	InstallationID int64      `json:"installation_id"`
	GitHubID       int64      `json:"github_id"`
	ZoektID        uint32     `json:"zoekt_id"`
	Name           string     `json:"name"`
	Branch         string     `json:"branch"`
	DesiredSHA     string     `json:"desired_sha"`
	IndexedSHA     string     `json:"indexed_sha"`
	WebURL         string     `json:"web_url"`
	Status         string     `json:"status"`
	ErrorCode      string     `json:"error_code"`
	SearchNode     string     `json:"search_node"`
	Enabled        bool       `json:"enabled"`
	LastIndexedAt  *time.Time `json:"last_indexed_at"`
}

type Registry interface {
	Repositories() []Repository
}

type Static struct{ repositories []Repository }

func NewStatic(repositories []Repository) (*Static, error) {
	if err := validate(repositories); err != nil {
		return nil, err
	}
	return &Static{repositories: copyRepositories(repositories)}, nil
}

func Load(path string, maxBytes int64) (*Static, error) {
	if maxBytes <= 0 || maxBytes == math.MaxInt64 {
		return nil, fmt.Errorf("%w: size limit must be positive", ErrInvalid)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: registry exceeds size limit", ErrInvalid)
	}
	var repositories []Repository
	if err := json.Unmarshal(data, &repositories); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return NewStatic(repositories)
}

func (registry *Static) Repositories() []Repository {
	return copyRepositories(registry.repositories)
}

func copyRepositories(repositories []Repository) []Repository {
	copy := append([]Repository(nil), repositories...)
	for index := range copy {
		if copy[index].LastIndexedAt != nil {
			indexedAt := *copy[index].LastIndexedAt
			copy[index].LastIndexedAt = &indexedAt
		}
	}
	return copy
}

func validate(repositories []Repository) error {
	ids := make(map[int64]struct{}, len(repositories))
	zoektIDs := make(map[uint32]struct{}, len(repositories))
	names := make(map[string]struct{}, len(repositories))
	for _, repository := range repositories {
		if repository.ID <= 0 || repository.ZoektID == 0 || repository.Name == "" {
			return fmt.Errorf("%w: repository id, zoekt id, and name are required", ErrInvalid)
		}
		if _, exists := ids[repository.ID]; exists {
			return fmt.Errorf("%w: duplicate repository id", ErrInvalid)
		}
		if _, exists := zoektIDs[repository.ZoektID]; exists {
			return fmt.Errorf("%w: duplicate zoekt id", ErrInvalid)
		}
		if _, exists := names[repository.Name]; exists {
			return fmt.Errorf("%w: duplicate repository name", ErrInvalid)
		}
		ids[repository.ID] = struct{}{}
		zoektIDs[repository.ZoektID] = struct{}{}
		names[repository.Name] = struct{}{}
	}
	return nil
}
