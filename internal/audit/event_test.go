package audit

import (
	"strings"
	"testing"
	"time"
)

func TestEventValidateBoundsFields(t *testing.T) {
	valid := Event{
		ActorType: "operator", ActorID: "recovery-admin",
		TargetType: "user", TargetID: "42",
		AuthenticationMethod: "local", Operation: "break_glass_password_set",
		Outcome: "success", RequestID: "request-1", CreatedAt: time.Now(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []Event{
		{ActorType: "unknown", TargetType: "user", Operation: "login", Outcome: "success"},
		{ActorType: "operator", TargetType: "secret", Operation: "login", Outcome: "success"},
		{ActorType: "operator", TargetType: "user", AuthenticationMethod: "password", Operation: "login", Outcome: "success"},
		{ActorType: "operator", TargetType: "user", Operation: "login", Outcome: "maybe"},
		{ActorType: "operator", ActorID: strings.Repeat("x", 129), TargetType: "user", Operation: "login", Outcome: "success"},
		{ActorType: "operator", TargetType: "user", Operation: strings.Repeat("x", 65), Outcome: "success"},
		{ActorType: "operator", TargetType: "user", Operation: "login\ncookie=value", Outcome: "success"},
	}
	for _, event := range tests {
		if err := event.Validate(); err == nil {
			t.Fatalf("accepted invalid event %#v", event)
		}
	}
}

func TestNewEventSetsTimestampAfterValidation(t *testing.T) {
	event, err := NewEvent(Event{
		ActorType: "system", TargetType: "authentication",
		Operation: "login_denied", Outcome: "denied",
	})
	if err != nil || event.CreatedAt.IsZero() {
		t.Fatalf("event=%#v err=%v", event, err)
	}
	if _, err := NewEvent(Event{ActorType: "secret"}); err == nil {
		t.Fatal("invalid event accepted")
	}
}
