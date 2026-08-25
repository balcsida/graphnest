package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/balcsida/graphnest/internal/observability"
	"github.com/balcsida/graphnest/internal/postgres"
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
	store             *postgres.Store
	reconcileRequests chan<- int64
	metrics           *observability.Metrics
}

func NewGitHubProcessor(store *postgres.Store, reconcileRequests chan<- int64, metricSet ...*observability.Metrics) *GitHubProcessor {
	var metrics *observability.Metrics
	if len(metricSet) > 0 {
		metrics = metricSet[0]
	}
	return &GitHubProcessor{store: store, reconcileRequests: reconcileRequests, metrics: metrics}
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
		SizeKB   *int64 `json:"size"`
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

func (processor *GitHubProcessor) Process(ctx context.Context, delivery Delivery) (inserted bool, resultErr error) {
	event := metricEvent(delivery.Event)
	result := "error"
	if processor.metrics != nil {
		defer func() { processor.metrics.ObserveWebhook(event, result) }()
	}
	if event == "unknown" {
		inserted, resultErr = processor.store.ApplyDelivery(ctx, postgres.Delivery{ID: delivery.ID, Event: delivery.Event, State: "ignored"}, nil)
		if resultErr == nil {
			if inserted {
				result = "ignored"
			} else {
				result = "duplicate"
			}
		}
		return inserted, resultErr
	}
	var payload eventPayload
	decodeErr := json.Unmarshal(delivery.Body, &payload)
	var installationID, repositoryID *int64
	if decodeErr == nil {
		if payload.Installation.ID > 0 {
			installationID = &payload.Installation.ID
		}
		if payload.Repository.ID > 0 && (delivery.Event == "push" || delivery.Event == "repository") {
			repositoryID = &payload.Repository.ID
		}
	}
	if decodeErr != nil || !validPayload(delivery.Event, payload) {
		inserted, resultErr = processor.invalidDelivery(ctx, delivery, installationID, repositoryID)
		if resultErr == nil && !inserted {
			result = "duplicate"
		}
		return inserted, resultErr
	}
	reconcile := delivery.Event == "installation" || delivery.Event == "installation_repositories" || delivery.Event == "repository"
	state := "ignored"
	if delivery.Event == "push" || reconcile {
		state = "accepted"
	}
	inserted, err := processor.store.ApplyDelivery(ctx, postgres.Delivery{
		ID: delivery.ID, Event: delivery.Event, State: state,
		InstallationID: installationID, RepositoryID: repositoryID,
	}, func(ctx context.Context, tx *postgres.DeliveryTx) error {
		switch delivery.Event {
		case "push":
			repository, err := tx.RepositoryForPush(ctx, payload.Installation.ID, payload.Repository.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if payload.Ref != "refs/heads/"+repository.Branch || !validSHA(payload.After) {
				return nil
			}
			if err := tx.UpdateRepositorySize(ctx, repository.ID, *payload.Repository.SizeKB*1024); err != nil {
				return err
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
	if !inserted {
		result = "duplicate"
		return false, nil
	}
	if reconcile && installationID != nil && processor.reconcileRequests != nil {
		select {
		case processor.reconcileRequests <- *installationID:
		default:
		}
	}
	result = state
	return inserted, nil
}

func (processor *GitHubProcessor) invalidDelivery(ctx context.Context, delivery Delivery, installationID, repositoryID *int64) (bool, error) {
	if processor.store == nil {
		return false, InvalidDeliveryError{}
	}
	inserted, err := processor.store.ApplyDelivery(ctx, postgres.Delivery{
		ID: delivery.ID, Event: delivery.Event, State: "failed", ErrorCode: "invalid_payload",
		InstallationID: installationID, RepositoryID: repositoryID,
	}, nil)
	if err != nil || !inserted {
		return inserted, err
	}
	return true, InvalidDeliveryError{}
}

func metricEvent(event string) string {
	if knownEvent(event) {
		return event
	}
	return "unknown"
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
		return payload.Repository.ID > 0 && payload.Repository.SizeKB != nil && *payload.Repository.SizeKB >= 0 && *payload.Repository.SizeKB <= math.MaxInt64/1024 &&
			strings.HasPrefix(payload.Ref, "refs/heads/") && payload.Ref != "refs/heads/" && validSHA(payload.After)
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
