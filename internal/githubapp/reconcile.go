package githubapp

import (
	"context"
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
	for _, installation := range installations {
		upstream[installation.ID] = struct{}{}
		if err := r.reconcile(ctx, installation); err != nil {
			return err
		}
	}
	for _, installationID := range local {
		if _, ok := upstream[installationID]; !ok {
			if err := r.store.DisableInstallation(ctx, installationID, "deleted"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Reconciler) reconcile(ctx context.Context, installation Installation) error {
	if installation.Status != "active" || installation.SuspendedAt != nil {
		return r.store.ReconcileInstallation(ctx, installation, nil)
	}
	repositories, err := r.github.InstallationRepositories(ctx, installation.ID)
	if err != nil {
		return err
	}
	for index := range repositories {
		repository := &repositories[index]
		if repository.Archived || repository.Disabled {
			continue
		}
		repository.DefaultSHA, err = r.github.DefaultBranchSHA(ctx, installation.ID, repository.Owner, repository.Name, repository.DefaultBranch)
		if err != nil {
			return fmt.Errorf("read %s default branch: %w", repository.FullName, err)
		}
	}
	return r.store.ReconcileInstallation(ctx, installation, repositories)
}
