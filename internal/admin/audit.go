package admin

import (
	"context"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
)

func (service *Service) AuditEvents(ctx context.Context, principal authn.Principal) ([]audit.Event, bool, error) {
	if err := requireIdentityAdmin(principal); err != nil {
		return nil, false, err
	}
	return service.Store.AuditEvents(ctx, service.limit())
}
