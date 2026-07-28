package graphservice

import (
	"context"
	"errors"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
)

var (
	ErrRepositoryNotFound        = errors.New("repository_not_found")
	ErrRepositoryRequired        = errors.New("repository_required")
	ErrInvalidRepositorySelector = errors.New("invalid_repository_selector")
	ErrBranchNotIndexed          = errors.New("branch_not_indexed")
	ErrGraphNotReady             = errors.New("graph_not_ready")
)

type RepositoryStore interface {
	GraphRepositories(context.Context, authn.Principal) ([]repository.Repository, error)
}

type Snapshot struct {
	ID       int64
	GitHubID int64
	Name     string
	Branch   string
	Commit   string
}

func ResolveRepository(ctx context.Context, store RepositoryStore, principal authn.Principal, selector api.GraphRepositorySelector, branch string) (Snapshot, error) {
	if selector.ID < 0 || selector.ID != 0 && selector.Name != "" {
		return Snapshot{}, ErrInvalidRepositorySelector
	}
	repositories, err := store.GraphRepositories(ctx, principal)
	if err != nil {
		return Snapshot{}, err
	}
	var selected *repository.Repository
	if selector.ID == 0 && selector.Name == "" {
		switch len(repositories) {
		case 0:
			return Snapshot{}, ErrRepositoryNotFound
		case 1:
			selected = &repositories[0]
		default:
			return Snapshot{}, ErrRepositoryRequired
		}
	} else {
		matches := 0
		for index := range repositories {
			candidate := &repositories[index]
			if selector.ID > 0 && candidate.GitHubID == selector.ID || selector.Name != "" && candidate.Name == selector.Name {
				selected = candidate
				matches++
			}
		}
		if selected == nil {
			return Snapshot{}, ErrRepositoryNotFound
		}
		if matches > 1 {
			return Snapshot{}, ErrRepositoryRequired
		}
	}
	if branch != "" && branch != selected.Branch {
		return Snapshot{}, ErrBranchNotIndexed
	}
	if selected.IndexedSHA == "" {
		return Snapshot{}, ErrGraphNotReady
	}
	return Snapshot{ID: selected.ID, GitHubID: selected.GitHubID, Name: selected.Name, Branch: selected.Branch, Commit: selected.IndexedSHA}, nil
}
