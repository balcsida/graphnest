package admin

import (
	"context"

	"github.com/grepnest/grepnest/internal/audit"
	"github.com/grepnest/grepnest/internal/authn"
)

func (service *Service) AuditEvents(ctx context.Context, principal authn.Principal) ([]audit.Event, bool, error) {
	if err := requireIdentityAdmin(principal); err != nil {
		return nil, false, err
	}
	return service.Store.AuditEvents(ctx, service.limit())
}
