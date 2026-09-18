package githubapp

import (
	"context"
	"errors"
	"fmt"
)

type ReconcileStore interface {
	InstallationIDs(context.Context) ([]int64, error)
	ReconcileInstallation(context.Context, Installation, []Repository) error
	DisableInstallation(context.Context, int64, string) error
}

type ReconcileAPI interface {
	Installations(context.Context) ([]Installation, error)
	InstallationRepositories(context.Context, int64) ([]Repository, error)
	DefaultBranchSHA(context.Context, int64, string, string, string) (string, error)
}

type Reconciler struct {
	github ReconcileAPI
	store  ReconcileStore
}

func NewReconciler(client *Client, store ReconcileStore) *Reconciler {
	return &Reconciler{github: client, store: store}
}

func (r *Reconciler) Installation(ctx context.Context, installationID int64) error {
	installations, err := r.github.Installations(ctx)
	if err != nil {
		return err
	}
	for _, installation := range installations {
		if installation.ID == installationID {
			return r.reconcile(ctx, installation)
		}
	}
	return r.store.DisableInstallation(ctx, installationID, "deleted")
}

func (r *Reconciler) All(ctx context.Context) error {
	installations, err := r.github.Installations(ctx)
	if err != nil {
		return err
	}
	local, err := r.store.InstallationIDs(ctx)
	if err != nil {
		return err
	}
	upstream := make(map[int64]struct{}, len(installations))
	var errs []error
	for _, installation := range installations {
		upstream[installation.ID] = struct{}{}
		if err := r.reconcile(ctx, installation); err != nil {
			errs = append(errs, err)
		}
	}
	for _, installationID := range local {
		if _, ok := upstream[installationID]; !ok {
			if err := r.store.DisableInstallation(ctx, installationID, "deleted"); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (r *Reconciler) reconcile(ctx context.Context, installation Installation) error {
	if installation.Status != "active" || installation.SuspendedAt != nil {
		return r.store.ReconcileInstallation(ctx, installation, nil)
	}
	repositories, err := r.github.InstallationRepositories(ctx, installation.ID)
	if err != nil {
		return err
	}
	var shaErrs []error
	for index := range repositories {
		repository := &repositories[index]
		if repository.Archived || repository.Disabled {
			continue
		}
		repository.DefaultSHA, err = r.github.DefaultBranchSHA(ctx, installation.ID, repository.Owner, repository.Name, repository.DefaultBranch)
		if err != nil {
			repository.DefaultSHA = ""
			repository.ErrorCode = "default_branch"
			name := repository.FullName
			if name == "" {
				name = repository.Owner + "/" + repository.Name
			}
			if status := httpStatus(err); status != 0 {
				shaErrs = append(shaErrs, fmt.Errorf("installation %d repository %s HTTP %d: read default branch: %w", installation.ID, name, status, err))
			} else {
				shaErrs = append(shaErrs, fmt.Errorf("installation %d repository %s: read default branch: %w", installation.ID, name, err))
			}
			continue
		}
	}
	if err := r.store.ReconcileInstallation(ctx, installation, repositories); err != nil {
		return err
	}
	return errors.Join(shaErrs...)
}

func httpStatus(err error) int {
	var status HTTPStatusError
	if errors.As(err, &status) {
		return status.StatusCode
	}
	return 0
}
