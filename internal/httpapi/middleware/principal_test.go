package middleware

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
)

func TestPrincipalContext(t *testing.T) {
	p := domain.Principal{UserID: uuid.New(), Role: domain.RoleAdmin, TokenID: uuid.New()}

	if _, ok := PrincipalFrom(context.Background()); ok {
		t.Error("empty context must have no principal")
	}
	ctx := WithPrincipal(context.Background(), p)
	got, ok := PrincipalFrom(ctx)
	if !ok || got != p {
		t.Errorf("PrincipalFrom = %+v, %v; want %+v", got, ok, p)
	}
	if MustPrincipal(ctx) != p {
		t.Error("MustPrincipal returned a different principal")
	}
}

func TestMustPrincipalPanicsWithoutPrincipal(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("want a panic")
		}
	}()
	MustPrincipal(context.Background())
}

func TestWithPrincipalReplacesEarlierPrincipal(t *testing.T) {
	a := domain.Principal{UserID: uuid.New(), Role: domain.RoleUser}
	b := domain.Principal{UserID: uuid.New(), Role: domain.RoleAdmin}
	ctx := WithPrincipal(WithPrincipal(context.Background(), a), b)
	if got, _ := PrincipalFrom(ctx); got != b {
		t.Errorf("got %+v, want %+v", got, b)
	}
}
