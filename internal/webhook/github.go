package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/jackc/pgx/v5"
)

type Delivery struct {
	ID, Event string
	Body      []byte
}

type Processor interface {
	Process(context.Context, Delivery) (bool, error)
}

type InvalidDeliveryError struct{}

func (InvalidDeliveryError) Error() string { return "invalid GitHub webhook delivery" }

func Verify(secret, body []byte, signature string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil || len(digest) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(digest, mac.Sum(nil))
}

type GitHubProcessor struct {
	store      *postgres.Store
	reconciler *githubapp.Reconciler
}

func NewGitHubProcessor(store *postgres.Store, reconciler *githubapp.Reconciler) *GitHubProcessor {
	return &GitHubProcessor{store: store, reconciler: reconciler}
}

type eventPayload struct {
	Action       string `json:"action"`
	Ref          string `json:"ref"`
	After        string `json:"after"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		CloneURL string `json:"clone_url"`
		HTMLURL  string `json:"html_url"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	RepositoriesRemoved []struct {
		ID int64 `json:"id"`
	} `json:"repositories_removed"`
}

func (processor *GitHubProcessor) Process(ctx context.Context, delivery Delivery) (bool, error) {
	if !knownEvent(delivery.Event) {
		return processor.store.ApplyDelivery(ctx, postgres.Delivery{ID: delivery.ID, Event: delivery.Event, State: "ignored"}, nil)
	}
	var payload eventPayload
	if err := json.Unmarshal(delivery.Body, &payload); err != nil || !validPayload(delivery.Event, payload) {
		return false, InvalidDeliveryError{}
	}
	var installationID *int64
	if payload.Installation.ID > 0 {
		installationID = &payload.Installation.ID
	}
	reconcile := delivery.Event == "installation" || delivery.Event == "installation_repositories" || delivery.Event == "repository"
	state := "ignored"
	if delivery.Event == "push" || reconcile {
		state = "accepted"
	}
	inserted, err := processor.store.ApplyDelivery(ctx, postgres.Delivery{
		ID: delivery.ID, Event: delivery.Event, State: state, InstallationID: installationID,
	}, func(ctx context.Context, tx *postgres.DeliveryTx) error {
		switch delivery.Event {
		case "push":
			repository, err := tx.RepositoryForPush(ctx, payload.Repository.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if payload.Ref != "refs/heads/"+repository.Branch || !validSHA(payload.After) {
				return nil
			}
			return tx.EnqueueIndex(ctx, postgres.IndexRequest{RepositoryID: repository.ID, TargetSHA: payload.After, TargetRef: payload.Ref, Reason: "push"})
		case "installation":
			if installationID != nil && (payload.Action == "deleted" || payload.Action == "suspend") {
				return tx.DisableInstallation(ctx, *installationID, map[string]string{"deleted": "deleted", "suspend": "suspended"}[payload.Action])
			}
		case "repository":
			if payload.Action == "deleted" || payload.Action == "archived" {
				return tx.DisableRepository(ctx, payload.Repository.ID, payload.Action)
			}
			if payload.Action == "renamed" {
				return tx.RenameRepository(ctx, payload.Repository.ID, payload.Repository.Owner.Login, payload.Repository.Name, payload.Repository.CloneURL, payload.Repository.HTMLURL)
			}
		case "installation_repositories":
			if payload.Action == "removed" {
				for _, repository := range payload.RepositoriesRemoved {
					if err := tx.DisableRepository(ctx, repository.ID, "removed"); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if inserted && reconcile && installationID != nil && processor.reconciler != nil {
		if err := processor.reconciler.Installation(ctx, *installationID); err != nil {
			return false, err
		}
	}
	return inserted, nil
}

func knownEvent(event string) bool {
	return event == "push" || event == "installation" || event == "installation_repositories" || event == "repository"
}

func validPayload(event string, payload eventPayload) bool {
	if payload.Installation.ID <= 0 {
		return false
	}
	switch event {
	case "push":
		return payload.Repository.ID > 0 && strings.HasPrefix(payload.Ref, "refs/heads/") && payload.Ref != "refs/heads/" && validSHA(payload.After)
	case "installation":
		return payload.Action != ""
	case "repository":
		if payload.Repository.ID <= 0 || payload.Action == "" {
			return false
		}
		return payload.Action != "renamed" || payload.Repository.Owner.Login != "" && payload.Repository.Name != "" && payload.Repository.CloneURL != "" && payload.Repository.HTMLURL != ""
	case "installation_repositories":
		if payload.Action == "" {
			return false
		}
		if payload.Action != "removed" {
			return true
		}
		if len(payload.RepositoriesRemoved) == 0 {
			return false
		}
		for _, repository := range payload.RepositoriesRemoved {
			if repository.ID <= 0 {
				return false
			}
		}
		return true
	}
	return false
}

func validSHA(sha string) bool {
	if len(sha) != 40 || sha == strings.Repeat("0", 40) {
		return false
	}
	for _, character := range sha {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}
