package graphscan

import (
	"context"
	"sync"
)

type budgetKey struct{}

type irBudget struct {
	mu           sync.Mutex
	nodes, edges int
	err          error
}

func withIRBudget(ctx context.Context, nodes, edges int) context.Context {
	return context.WithValue(ctx, budgetKey{}, &irBudget{nodes: nodes, edges: edges})
}

func Add[T any](ctx context.Context, values *[]T, value T) bool {
	budget, _ := ctx.Value(budgetKey{}).(*irBudget)
	if budget == nil {
		*values = append(*values, value)
		return true
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	nodeCost := 0
	if _, ok := any(value).(Declaration); ok {
		nodeCost = 1
	}
	if budget.err != nil || budget.nodes < nodeCost || budget.edges < 1 {
		budget.err = ErrLimitExceeded
		return false
	}
	budget.nodes -= nodeCost
	budget.edges--
	*values = append(*values, value)
	return true
}

func BudgetError(ctx context.Context) error {
	budget, _ := ctx.Value(budgetKey{}).(*irBudget)
	if budget == nil {
		return nil
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.err
}
