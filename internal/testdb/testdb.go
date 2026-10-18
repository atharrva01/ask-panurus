// Package testdb gives each test package its own throwaway Postgres
// database, so tests never share state or touch a real index.
package testdb

import (
	"context"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// URL creates a fresh database called name on the server named by
// ASK_PANURUS_TEST_DB, drops it when the test ends, and returns its URL.
// The test is skipped when ASK_PANURUS_TEST_DB is not set:
//
//	docker compose -f deploy/docker-compose.yml up -d postgres
//	ASK_PANURUS_TEST_DB=postgres://ask:ask@localhost:5433/postgres go test ./...
func URL(t *testing.T, name string) string {
	t.Helper()
	admin := os.Getenv("ASK_PANURUS_TEST_DB")
	if admin == "" {
		t.Skip("ASK_PANURUS_TEST_DB not set")
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)")
		conn.Close(ctx)
	})
	u.Path = "/" + name
	return u.String()
}
