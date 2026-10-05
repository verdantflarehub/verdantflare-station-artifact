// Package migrations owns Artifact tables only, independently of Core migrations.
package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var files embed.FS

const Version = 1

func Apply(ctx context.Context, pool *pgxpool.Pool, storeID string) error {
	return run(ctx, pool, storeID, true)
}
func Check(ctx context.Context, pool *pgxpool.Pool, storeID string) error {
	return run(ctx, pool, storeID, false)
}

func run(ctx context.Context, pool *pgxpool.Pool, storeID string, apply bool) error {
	id, err := uuid.Parse(storeID)
	if err != nil || id.Version() != 7 || id.String() != storeID {
		return errors.New("store ID must be lowercase UUIDv7")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(2026100501)"); err != nil {
		return err
	}
	if apply {
		if _, err = tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS station;
CREATE TABLE IF NOT EXISTS station.artifact_schema_migrations(version integer PRIMARY KEY, name text NOT NULL, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now());`); err != nil {
			return err
		}
	}
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return err
	}
	if len(names) != Version {
		return errors.New("migration source version mismatch")
	}
	rows, err := tx.Query(ctx, "SELECT version,name,checksum FROM station.artifact_schema_migrations ORDER BY version")
	if err != nil {
		return err
	}
	n := 0
	for rows.Next() {
		var v int
		var name, sum string
		if err = rows.Scan(&v, &name, &sum); err != nil {
			rows.Close()
			return err
		}
		if n >= len(names) || v != n+1 || name != names[n] {
			rows.Close()
			return errors.New("migration history mismatch")
		}
		sql, _ := files.ReadFile(name)
		digest := sha256.Sum256(sql)
		if hex.EncodeToString(digest[:]) != sum {
			rows.Close()
			return errors.New("migration checksum mismatch")
		}
		n++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !apply && n != len(names) {
		return errors.New("pending Artifact migrations")
	}
	for i, name := range names[n:] {
		if !strings.HasPrefix(name, fmt.Sprintf("%04d_", n+i+1)) {
			return errors.New("unexpected migration sequence")
		}
		sql, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			return err
		}
		digest := sha256.Sum256(sql)
		if _, err = tx.Exec(ctx, "INSERT INTO station.artifact_schema_migrations(version,name,checksum) VALUES($1,$2,$3)", n+i+1, name, hex.EncodeToString(digest[:])); err != nil {
			return err
		}
	}
	if apply {
		if _, err = tx.Exec(ctx, "INSERT INTO station.artifact_stores(store_id) VALUES($1) ON CONFLICT(singleton) DO NOTHING", storeID); err != nil {
			return err
		}
	}
	var actual string
	if err = tx.QueryRow(ctx, "SELECT store_id::text FROM station.artifact_stores WHERE singleton").Scan(&actual); err != nil {
		return err
	}
	if actual != storeID {
		return errors.New("Artifact store identity mismatch")
	}
	return tx.Commit(ctx)
}
