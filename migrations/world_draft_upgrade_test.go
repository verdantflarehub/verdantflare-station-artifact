package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/testdb"
)

func TestWorldDraftUpgradePreservesExistingStoreAndRetention(t *testing.T) {
	db := testdb.New(t)
	ctx := t.Context()
	initial, err := files.ReadFile("0001_content_versions.sql")
	if err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(initial)
	if _, err = db.Exec(ctx, `CREATE SCHEMA station;
CREATE TABLE station.artifact_schema_migrations(version integer PRIMARY KEY, name text NOT NULL, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now());`+string(initial)); err != nil {
		t.Fatal(err)
	}
	store := id(t)
	if _, err = db.Exec(ctx, "INSERT INTO station.artifact_stores(store_id) VALUES($1)", store); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, "INSERT INTO station.artifact_schema_migrations(version,name,checksum) VALUES(1,'0001_content_versions.sql',$1)", hex.EncodeToString(checksum[:])); err != nil {
		t.Fatal(err)
	}
	org, owner, commit := id(t), id(t), id(t)
	for _, kind := range []string{"project_revision", "asset_version", "pending_commit"} {
		if _, err = db.Exec(ctx, "INSERT INTO station.artifact_retention_owners(organization_id,owner_kind,owner_id,commit_id,request_sha256) VALUES($1,$2,$3,$4,$5)", org, kind, owner, commit, strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
	}
	var before string
	query := "SELECT jsonb_agg(to_jsonb(r) ORDER BY owner_kind)::text FROM station.artifact_retention_owners r"
	if err = db.QueryRow(ctx, query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if Check(ctx, db, store) == nil {
		t.Fatal("new runtime accepted unmigrated schema")
	}
	for i := 0; i < 2; i++ {
		if err = Apply(ctx, db, store); err != nil {
			t.Fatal("upgrade or replay failed", err)
		}
	}
	if err = Check(ctx, db, store); err != nil {
		t.Fatal(err)
	}
	var saved string
	var count int
	if err = db.QueryRow(ctx, "SELECT checksum FROM station.artifact_schema_migrations WHERE version=1").Scan(&saved); err != nil || saved != hex.EncodeToString(checksum[:]) {
		t.Fatal("initial history changed", err)
	}
	if err = db.QueryRow(ctx, "SELECT count(*) FROM station.artifact_schema_migrations").Scan(&count); err != nil || count != 3 {
		t.Fatal("migration replay duplicated history", err)
	}
	var currentStore string
	if err = db.QueryRow(ctx, "SELECT store_id::text FROM station.artifact_stores WHERE singleton").Scan(&currentStore); err != nil || currentStore != store {
		t.Fatal("store identity changed after upgrade", err)
	}
}
