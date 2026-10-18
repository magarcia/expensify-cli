package expensify

import (
	"context"
	"strconv"
)

func (s *Service) findExpense(ctx context.Context, id string) (Expense, error) {
	state, err := s.findExpenseSnapshot(ctx, id)
	if err != nil {
		return Expense{}, err
	}
	return expenseInSnapshot(state, id, object(state["transactions_"+id]))
}

func (s *Service) findExpenseSnapshot(ctx context.Context, id string) (snapshot, error) {
	for offset := 0; ; offset += 50 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		state, _, hasMore, err := s.search(ctx, "expense", map[string]string{"limit": "50", "offset": strconv.Itoa(offset)})
		if err != nil {
			return nil, err
		}
		if raw := object(state["transactions_"+id]); raw != nil {
			return state, nil
		}
		if !hasMore {
			return nil, notFound("Expense", id)
		}
	}
}
