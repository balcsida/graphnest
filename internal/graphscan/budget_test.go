package graphscan

import (
	"context"
	"errors"
	"testing"
)

func TestBudgetErrorReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(withIRBudget(t.Context(), 1, 1))
	cancel()

	if err := BudgetError(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("BudgetError() = %v, want context.Canceled", err)
	}
}
