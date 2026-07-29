package audit

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidEvent = errors.New("invalid audit event")

type Event struct {
	ActorType, ActorID, TargetType, TargetID            string
	AuthenticationMethod, Operation, Outcome, RequestID string
	CreatedAt                                           time.Time
}

func NewEvent(event Event) (Event, error) {
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	return event, nil
}

func (event Event) Validate() error {
	if !oneOf(event.ActorType, "anonymous", "operator", "scim", "system", "user") ||
		!oneOf(event.TargetType, "api_token", "authentication", "group", "session", "user") ||
		!oneOf(event.AuthenticationMethod, "", "api_token", "local", "oidc", "operator", "scim_token") ||
		!oneOf(event.Outcome, "success", "denied", "invalid", "error") ||
		!bounded(event.ActorID, 128) || !bounded(event.TargetID, 128) ||
		!bounded(event.RequestID, 128) || !operation(event.Operation) {
		return ErrInvalidEvent
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func bounded(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func operation(value string) bool {
	if value == "" || len(value) > 64 || strings.Trim(value, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return false
	}
	return value[0] != '_' && value[len(value)-1] != '_'
}
