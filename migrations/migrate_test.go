package migrations

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/testdb"
)

func id(t *testing.T) string {
	t.Helper()
	u, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return u.String()
}

func TestExplicitMigrationIdentityAndChecksum(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	store := id(t)
	if Check(ctx, pool, store) == nil {
		t.Fatal("uninitialized store accepted")
	}
	if err := Apply(ctx, pool, store); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, pool, store); err != nil {
		t.Fatal("repeat migration", err)
	}
	if err := Check(ctx, pool, store); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, pool, id(t)); err == nil {
		t.Fatal("store identity silently changed")
	}
	if _, err := pool.Exec(ctx, "UPDATE station.artifact_schema_migrations SET checksum='invalid'"); err != nil {
		t.Fatal(err)
	}
	if Check(ctx, pool, store) == nil || Apply(ctx, pool, store) == nil {
		t.Fatal("tampered migration accepted")
	}
}

func TestImmutableMetadataAndOrganizationConstraints(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	if err := Apply(ctx, pool, id(t)); err != nil {
		t.Fatal(err)
	}
	org, other, user, artifact, object, version := id(t), id(t), id(t), id(t), id(t), id(t)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO station.artifact_artifacts(artifact_id,organization_id,created_by) VALUES($1,$2,$3)", artifact, org, user)
	exec("INSERT INTO station.artifact_objects(object_id,organization_id,storage_key,sha256,size) VALUES($1,$2,$3,$4,3)", object, org, "objects/"+object, strings.Repeat("a", 64))
	exec("INSERT INTO station.artifact_versions(version_id,artifact_id,organization_id,version_no,object_id,source,sha256,size,mime,created_by) VALUES($1,$2,$3,1,$4,$5,$6,3,'text/plain',$7)", version, artifact, org, object, `{"kind":"user_import","project_id":"`+id(t)+`"}`, strings.Repeat("a", 64), user)
	for _, query := range []string{"UPDATE station.artifact_versions SET mime='text/markdown'", "TRUNCATE station.artifact_versions CASCADE", "UPDATE station.artifact_objects SET size=4"} {
		if _, err := pool.Exec(ctx, query); err == nil {
			t.Fatalf("immutable mutation allowed: %s", query)
		}
	}
	if _, err := pool.Exec(ctx, "INSERT INTO station.artifact_versions(version_id,artifact_id,organization_id,version_no,object_id,source,sha256,size,mime,created_by) VALUES($1,$2,$3,2,$4,$5,$6,3,'text/plain',$7)", id(t), artifact, other, object, `{"kind":"user_import","project_id":"`+id(t)+`"}`, strings.Repeat("a", 64), user); err == nil {
		t.Fatal("cross-organization version/object linkage allowed")
	}
	exec("INSERT INTO station.artifact_events(event_id,organization_id,subject_id,action,resource_id,request_id) VALUES($1,$2,$3,'created',$4,$5)", id(t), org, user, version, id(t))
	if _, err := pool.Exec(ctx, "DELETE FROM station.artifact_events"); err == nil {
		t.Fatal("audit events could be deleted")
	}
	exec("DELETE FROM station.artifact_versions WHERE organization_id=$1 AND version_id=$2", org, version)
	exec("DELETE FROM station.artifact_objects WHERE organization_id=$1 AND object_id=$2", org, object)
}
