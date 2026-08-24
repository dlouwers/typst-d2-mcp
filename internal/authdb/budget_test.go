package authdb

import (
	"errors"
	"testing"
)

func TestWorkspaceBudget_SetFixedUnlimitedAndDefault(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	userID := seedUser(t, s, 42, "octocat")

	// No override: inherits the passed-in default.
	if got, err := s.EffectiveWorkspaceBudget(ctx, userID, 1000); err != nil || got != 1000 {
		t.Errorf("EffectiveWorkspaceBudget with no override = %d, %v; want 1000", got, err)
	}

	fifty := int64(50 << 20)
	if err := s.SetWorkspaceBudget(ctx, "dlouwers", "octocat", &fifty); err != nil {
		t.Fatalf("SetWorkspaceBudget: %v", err)
	}
	if got, _ := s.EffectiveWorkspaceBudget(ctx, userID, 1000); got != fifty {
		t.Errorf("EffectiveWorkspaceBudget after override = %d, want %d", got, fifty)
	}

	// 0 is unlimited and must survive the round trip, not read as "unset".
	zero := int64(0)
	if err := s.SetWorkspaceBudget(ctx, "dlouwers", "octocat", &zero); err != nil {
		t.Fatalf("SetWorkspaceBudget unlimited: %v", err)
	}
	if got, _ := s.EffectiveWorkspaceBudget(ctx, userID, 1000); got != 0 {
		t.Errorf("EffectiveWorkspaceBudget unlimited = %d, want 0", got)
	}

	// nil clears the override, restoring the default.
	if err := s.SetWorkspaceBudget(ctx, "dlouwers", "octocat", nil); err != nil {
		t.Fatalf("SetWorkspaceBudget default: %v", err)
	}
	if got, _ := s.EffectiveWorkspaceBudget(ctx, userID, 1000); got != 1000 {
		t.Errorf("EffectiveWorkspaceBudget after clearing = %d, want 1000", got)
	}
}

func TestWorkspaceBudget_SetOnUnknownUser(t *testing.T) {
	s := newStore(t)
	n := int64(1024)
	err := s.SetWorkspaceBudget(t.Context(), "dlouwers", "ghost", &n)
	if !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("err = %v, want ErrNoSuchUser", err)
	}
}

// An unknown workspace must fall back to the default rather than erroring,
// so a put_file racing a deletion fails open on the default.
func TestWorkspaceBudget_UnknownFallsBackToDefault(t *testing.T) {
	s := newStore(t)
	got, err := s.EffectiveWorkspaceBudget(t.Context(), "gh:999", 4096)
	if err != nil {
		t.Fatalf("EffectiveWorkspaceBudget: %v", err)
	}
	if got != 4096 {
		t.Errorf("budget = %d, want the default 4096", got)
	}
}

func TestWorkspaceBudget_SurfacedInAdminUsers(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	seedUser(t, s, 7, "octocat")
	budget := int64(10 << 20)
	if err := s.SetWorkspaceBudget(ctx, "dlouwers", "octocat", &budget); err != nil {
		t.Fatalf("SetWorkspaceBudget: %v", err)
	}
	rows, err := s.AdminUsers(ctx, today)
	if err != nil {
		t.Fatalf("AdminUsers: %v", err)
	}
	if len(rows) != 1 || rows[0].BudgetOverride == nil {
		t.Fatalf("expected one row with a budget override, got %+v", rows)
	}
	if *rows[0].BudgetOverride != budget {
		t.Errorf("BudgetOverride = %d, want %d", *rows[0].BudgetOverride, budget)
	}
}
