// Package testdb creates uniquely named disposable PostgreSQL databases.
package testdb

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConnString includes the generated database; pgx keeps the original admin DSN
// when a config's Database field is subsequently changed.
func ConnString(pool *pgxpool.Pool) string {
	cfg := pool.Config()
	u, err := url.Parse(cfg.ConnString())
	if err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		u.Path = "/" + cfg.ConnConfig.Database
		u.RawPath = ""
		q := u.Query()
		q.Del("dbname")
		q.Del("database")
		u.RawQuery = q.Encode()
		return u.String()
	}
	return cfg.ConnString() + " dbname=" + cfg.ConnConfig.Database
}

func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("ARTIFACT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ARTIFACT_TEST_DATABASE_URL required for real PostgreSQL integration")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	name := "artifact_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal("create disposable test database:", err)
	}
	cfg := admin.Config()
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		admin.Close()
		t.Fatal("connect test database:", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		admin.Close()
		if err != nil {
			t.Error("drop disposable database:", err)
		}
	})
	return pool
}
