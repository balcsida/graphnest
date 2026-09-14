package authn

import "context"

type freshPrincipalKey struct{}

type FreshPrincipalFunc func(context.Context) (Principal, error)

func WithFreshPrincipal(ctx context.Context, authenticate FreshPrincipalFunc) context.Context {
	return context.WithValue(ctx, freshPrincipalKey{}, authenticate)
}

func FreshPrincipal(ctx context.Context, fallback Principal) (Principal, error) {
	if authenticate, ok := ctx.Value(freshPrincipalKey{}).(FreshPrincipalFunc); ok {
		return authenticate(ctx)
	}
	return fallback, nil
}
