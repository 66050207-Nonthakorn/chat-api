package auth_test

import (
	"context"
	"testing"

	"cloudchat/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSessionRulesValidatePrincipalBeforeQuery(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://127.0.0.1:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	const id = "11111111-1111-4111-8111-111111111111"
	rules := auth.NewSessionRules(pool)
	for _, check := range []struct {
		name string
		run  func(context.Context, auth.Principal) (bool, error)
	}{{"Current", rules.Current}, {"TouchIdle", rules.TouchIdle}} {
		for _, principal := range []auth.Principal{
			{UserID: "invalid", SessionID: id},
			{UserID: id, SessionID: "invalid"},
		} {
			if valid, err := check.run(ctx, principal); valid || err != nil {
				t.Errorf("%s(%+v) = %v, %v; want false, nil", check.name, principal, valid, err)
			}
		}
	}
	missingPool := auth.NewSessionRules(nil)
	if valid, err := missingPool.Current(ctx, auth.Principal{}); valid || err == nil {
		t.Errorf("Current with no pool = %v, %v; want false and an error", valid, err)
	}
	if valid, err := missingPool.TouchIdle(ctx, auth.Principal{}); valid || err == nil {
		t.Errorf("TouchIdle with no pool = %v, %v; want false and an error", valid, err)
	}
}
