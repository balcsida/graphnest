//go:build integration

package postgres

import (
	"strconv"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
)

func testOAuthRefreshAudit(userID, grantID int64) audit.Event {
	return audit.Event{
		ActorType: "user", ActorID: strconv.FormatInt(userID, 10), TargetType: "oauth_grant",
		TargetID: strconv.FormatInt(grantID, 10), AuthenticationMethod: authn.ProviderOAuthToken,
		Operation: audit.OperationOAuthGrantRefreshed, Outcome: "success",
	}
}

func TestOAuthRefreshAuditCommitsWithRotation(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := insertIdentityUser(t, store, "refresh-audit", "refresh-audit")
	client := seedOAuthClient(t, store, now)
	grantID, err := store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{
		ClientID: client.ID, UserID: userID, AccessHash: [32]byte{1}, AccessExpiresAt: now.Add(time.Hour),
		RefreshHash: [32]byte{2}, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	event := testOAuthRefreshAudit(userID, grantID)
	event.RequestID = "request-42"
	rotation := authn.OAuthRotation{
		AccessHash: [32]byte{3}, AccessExpiresAt: now.Add(2 * time.Hour), RefreshHash: [32]byte{4},
		Now: now.Add(time.Minute), Grace: 30 * time.Second, Audit: event,
	}

	failed := rotation
	failed.Audit.CreatedAt = time.Date(1_000_000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := store.RotateOAuthGrant(t.Context(), [32]byte{2}, failed); err == nil {
		t.Fatal("refresh rotation survived audit insert failure")
	}
	current, err := store.OAuthGrantByRefresh(t.Context(), [32]byte{2}, rotation.Now)
	if err != nil || current.AccessHash != [32]byte{1} {
		t.Fatalf("failed audit changed current grant: grant=%+v err=%v", current, err)
	}
	var consumed, events int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from oauth_refresh_tokens`).Scan(&consumed); err != nil || consumed != 0 {
		t.Fatalf("failed audit retained consumed token: count=%d err=%v", consumed, err)
	}
	if err := store.pool.QueryRow(t.Context(), `select count(*) from audit_events`).Scan(&events); err != nil || events != 0 {
		t.Fatalf("failed audit retained event: count=%d err=%v", events, err)
	}

	if _, err := store.RotateOAuthGrant(t.Context(), [32]byte{2}, rotation); err != nil {
		t.Fatalf("retry refresh: %v", err)
	}
	audits, _, err := store.AuditEvents(t.Context(), 10)
	if err != nil || len(audits) != 1 || audits[0].ActorType != event.ActorType || audits[0].ActorID != event.ActorID ||
		audits[0].TargetType != event.TargetType || audits[0].TargetID != event.TargetID ||
		audits[0].AuthenticationMethod != event.AuthenticationMethod || audits[0].Operation != event.Operation ||
		audits[0].Outcome != event.Outcome || audits[0].RequestID != event.RequestID {
		t.Fatalf("refresh audits=%#v err=%v", audits, err)
	}
}
