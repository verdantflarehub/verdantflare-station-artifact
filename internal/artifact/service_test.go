package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/storage"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/testdb"
	"github.com/verdantflarehub/verdantflare-station-artifact/migrations"
)

func testID(t *testing.T) string {
	t.Helper()
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type testPolicy struct {
	org, user, project, guest, asset, assetVersion, visibleVersion string
	release                                                        atomic.Bool
	deny                                                           atomic.Bool
}

func (a *testPolicy) Authorize(_ context.Context, p Principal, perm Permission) error {
	if a.deny.Load() || p.OrganizationID != a.org {
		return ErrForbidden
	}
	if perm.Action == "read" && p.SubjectID == a.guest && perm.Version != nil && perm.Access != nil && perm.Version.VersionID == a.visibleVersion && perm.Access.AssetID == a.asset && perm.Access.AssetVersionID == a.assetVersion {
		return nil
	}
	if p.SubjectID != a.user {
		return ErrForbidden
	}
	if perm.Action == "write" && perm.Source.ProjectID != a.project {
		return ErrForbidden
	}
	if perm.Action == "release" && !a.release.Load() {
		return ErrForbidden
	}
	return nil
}

type fixture struct {
	s          *Service
	db         *pgxpool.Pool
	blobs      *storage.Local
	p          Principal
	policy     *testPolicy
	dir, store string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	db := testdb.New(t)
	store := testID(t)
	ctx := context.Background()
	if err := migrations.Apply(ctx, db, store); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	blobs, err := storage.NewLocal(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { blobs.Close() })
	p := Principal{testID(t), testID(t), testID(t)}
	policy := &testPolicy{org: p.OrganizationID, user: p.SubjectID, project: testID(t), guest: testID(t), asset: testID(t), assetVersion: testID(t)}
	s, err := New(ctx, db, blobs, policy, store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{s, db, blobs, p, policy, dir, store}
}
func (f *fixture) request(t *testing.T, data []byte) PrepareRequest {
	h := sha256.Sum256(data)
	return PrepareRequest{WriteID: testID(t), Source: Source{Kind: "user_edit", ProjectID: f.policy.project}, SHA256: hex.EncodeToString(h[:]), Size: int64(len(data)), MIME: "text/markdown"}
}
func (f *fixture) save(t *testing.T, data []byte) Version {
	t.Helper()
	ctx := context.Background()
	u, err := f.s.Prepare(ctx, f.p, f.request(t, data))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Put(ctx, f.p, u.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	v, err := f.s.Commit(ctx, f.p, u.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func count(t *testing.T, db *pgxpool.Pool, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLegacyMappingAndNativeIDs(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	data := []byte("historical result")
	r := f.request(t, data)
	r.Source = Source{Kind: "legacy_import", ProjectID: f.policy.project, Original: &OriginalRef{ServiceID: "music", ArtifactID: "native-track-7", VersionID: "master-v1", TaskID: "native-job-42"}}
	if _, err := f.s.ResolveLegacy(ctx, f.p, r.Source); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unmapped result: %v", err)
	}
	u, err := f.s.Prepare(ctx, f.p, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Put(ctx, f.p, u.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	r2 := r
	r2.WriteID = testID(t)
	original := *r.Source.Original
	original.TaskID = "different-legacy-task-annotation"
	r2.Source.Original = &original
	u2, err := f.s.Prepare(ctx, f.p, r2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Put(ctx, f.p, u2.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		v   Version
		err error
	}
	results := make(chan outcome, 2)
	for _, upload := range []Upload{u, u2} {
		go func() { v, e := f.s.Commit(ctx, f.p, upload.UploadID); results <- outcome{v, e} }()
	}
	var winner Version
	conflicts := 0
	for range 2 {
		out := <-results
		if out.err == nil {
			winner = out.v
		} else if errors.Is(out.err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(out.err)
		}
	}
	if conflicts != 1 || winner.VersionID == "" {
		t.Fatal("legacy mapping race did not converge")
	}
	resolved, err := f.s.ResolveLegacy(ctx, f.p, r.Source)
	if err != nil || resolved.ContentRef != winner.ContentRef {
		t.Fatalf("existing mapping not reused: %v", err)
	}
	f.policy.deny.Store(true)
	if _, err = f.s.ResolveLegacy(ctx, f.p, r.Source); !errors.Is(err, ErrForbidden) {
		t.Fatal("legacy mapping bypassed authorization")
	}
	f.policy.deny.Store(false)
	native := f.request(t, []byte("native result"))
	native.Source = Source{Kind: "task_output", ProjectID: f.policy.project, ServiceID: "video", RunID: "provider-job/opaque:007"}
	nu, err := f.s.Prepare(ctx, f.p, native)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Put(ctx, f.p, nu.UploadID, bytes.NewReader([]byte("native result"))); err != nil {
		t.Fatal(err)
	}
	nv, err := f.s.Commit(ctx, f.p, nu.UploadID)
	if err != nil || nv.Source.RunID != native.Source.RunID || nv.Source.TaskID != "" {
		t.Fatalf("native IDs rewritten: %v", err)
	}
}

func TestRetentionRequiresActualContent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	v := f.save(t, []byte("original"))
	if err := os.WriteFile(filepath.Join(f.dir, "objects", v.ObjectID), []byte("corrupt!"), 0600); err != nil {
		t.Fatal(err)
	}
	request := RetainRequest{Owner: Owner{Kind: "project_revision", ID: testID(t), CommitID: testID(t)}, Refs: []ContentRef{v.ContentRef}}
	if _, err := f.s.Retain(ctx, f.p, request); !errors.Is(err, ErrNotReady) {
		t.Fatalf("corrupt object retained: %v", err)
	}
	if count(t, f.db, "SELECT count(*) FROM station.artifact_retention_owners") != 0 {
		t.Fatal("failed integrity check left retention owner")
	}
}

func TestPrepareRetryAndConflictingDeclaration(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := f.request(t, []byte("# 小月"))
	results := make(chan Upload, 10)
	errs := make(chan error, 10)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() { defer wg.Done(); u, err := f.s.Prepare(ctx, f.p, r); results <- u; errs <- err }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var original Upload
	for u := range results {
		if original.UploadID == "" {
			original = u
		}
		if original.UploadID != u.UploadID || original.VersionID != u.VersionID || original.ArtifactID != u.ArtifactID {
			t.Fatal("retry allocated new identities")
		}
	}
	if count(t, f.db, "SELECT count(*) FROM station.artifact_artifacts") != 1 || count(t, f.db, "SELECT count(*) FROM station.artifact_uploads") != 1 {
		t.Fatal("duplicate initialization")
	}
	r.MIME = "text/plain"
	if _, err := f.s.Prepare(ctx, f.p, r); !errors.Is(err, ErrConflict) {
		t.Fatal("changed declaration accepted", err)
	}
	other := f.p
	other.SubjectID = f.policy.guest
	if _, err := f.s.Upload(ctx, other, original.UploadID); !errors.Is(err, ErrNotFound) {
		t.Fatal("upload leaked to other actor", err)
	}
}

func TestCommitRollbackRestartAndResponseLoss(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	data := []byte("# review\n选定参考。\n")
	r := f.request(t, data)
	u, err := f.s.Prepare(ctx, f.p, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Commit(ctx, f.p, u.UploadID); !errors.Is(err, ErrNotReady) {
		t.Fatal("missing object published", err)
	}
	if _, err = f.s.Put(ctx, f.p, u.UploadID, bytes.NewReader([]byte("wrong"))); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad bytes accepted", err)
	}
	if _, err = f.s.Put(ctx, f.p, u.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	ref := ContentRef{f.store, u.ArtifactID, u.VersionID}
	if _, err = f.s.Get(ctx, f.p, ref, Access{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("uncommitted bytes visible", err)
	}
	_, err = f.db.Exec(ctx, `CREATE FUNCTION station.inject_commit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='upload.commit' THEN RAISE EXCEPTION 'injected failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER inject_commit_failure BEFORE INSERT ON station.artifact_events FOR EACH ROW EXECUTE FUNCTION station.inject_commit_failure();`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Commit(ctx, f.p, u.UploadID); err == nil {
		t.Fatal("injected failure ignored")
	}
	if count(t, f.db, "SELECT count(*) FROM station.artifact_versions") != 0 || count(t, f.db, "SELECT count(*) FROM station.artifact_objects") != 0 {
		t.Fatal("partial metadata escaped transaction")
	}
	if _, err = f.db.Exec(ctx, "DROP TRIGGER inject_commit_failure ON station.artifact_events"); err != nil {
		t.Fatal(err)
	}
	// Recreate both the database pool and storage/service instances before retry.
	pool, err := pgxpool.NewWithConfig(ctx, f.db.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	blobs, err := storage.NewLocal(f.dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Close()
	restarted, err := New(ctx, pool, blobs, f.policy, f.store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	v, err := restarted.Commit(ctx, f.p, u.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	if v.VersionID != u.VersionID || v.VersionNo != 1 {
		t.Fatal("identity changed on recovery")
	}
	for range 3 {
		again, err := restarted.Commit(ctx, f.p, u.UploadID)
		if err != nil || again.VersionID != v.VersionID || !again.CreatedAt.Equal(v.CreatedAt) {
			t.Fatal("response-loss retry changed result", err)
		}
	}
	if count(t, f.db, "SELECT count(*) FROM station.artifact_events WHERE action='upload.commit'") != 1 {
		t.Fatal("commit audit duplicated")
	}
	_, file, err := restarted.Open(ctx, f.p, ref, Access{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	file.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("persisted bytes changed", err)
	}
}

func TestVersionAppendAndIndependentAssetAuthorization(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	v1 := f.save(t, []byte("approved"))
	candidate := f.save(t, []byte("private rejected candidate"))
	r := f.request(t, []byte("new version"))
	r.ArtifactID = v1.ArtifactID
	u, err := f.s.Prepare(ctx, f.p, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Put(ctx, f.p, u.UploadID, bytes.NewReader([]byte("new version"))); err != nil {
		t.Fatal(err)
	}
	v2, err := f.s.Commit(ctx, f.p, u.UploadID)
	if err != nil || v2.VersionNo != 2 || v2.ArtifactID != v1.ArtifactID || v2.VersionID == v1.VersionID {
		t.Fatal("append version failed", err)
	}
	if old, err := f.s.Get(ctx, f.p, v1.ContentRef, Access{}); err != nil || old.SHA256 != v1.SHA256 {
		t.Fatal("old version changed", err)
	}
	f.policy.visibleVersion = v1.VersionID
	guest := f.p
	guest.SubjectID = f.policy.guest
	access := Access{AssetID: f.policy.asset, AssetVersionID: f.policy.assetVersion}
	if _, err = f.s.Get(ctx, guest, v1.ContentRef, access); err != nil {
		t.Fatal("asset grant did not allow selected file", err)
	}
	if _, err = f.s.Get(ctx, guest, candidate.ContentRef, access); !errors.Is(err, ErrForbidden) {
		t.Fatal("asset grant exposed source candidate", err)
	}
	if _, err = f.s.Get(ctx, guest, v1.ContentRef, Access{}); !errors.Is(err, ErrForbidden) {
		t.Fatal("asset grant implicitly granted source access", err)
	}
	other := f.p
	other.OrganizationID = testID(t)
	if _, err = f.s.Get(ctx, other, v1.ContentRef, Access{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-organization reference leaked", err)
	}
	bad := v1.ContentRef
	bad.ArtifactID = candidate.ArtifactID
	if _, err = f.s.Get(ctx, f.p, bad, Access{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("version/artifact mismatch accepted", err)
	}
}

func TestRetentionAtomicityReleaseAndDeletion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a := f.save(t, []byte("A"))
	b := f.save(t, []byte("B"))
	o := Owner{"asset_version", testID(t), testID(t)}
	missing := a.ContentRef
	missing.VersionID = testID(t)
	if _, err := f.s.Retain(ctx, f.p, RetainRequest{o, []ContentRef{a.ContentRef, missing}}); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing content retained", err)
	}
	if count(t, f.db, "SELECT count(*) FROM station.artifact_retention_owners") != 0 {
		t.Fatal("partial retention published")
	}
	if _, err := f.s.Retain(ctx, f.p, RetainRequest{o, []ContentRef{b.ContentRef, a.ContentRef}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Retain(ctx, f.p, RetainRequest{o, []ContentRef{a.ContentRef, b.ContentRef}}); err != nil {
		t.Fatal("set ordering broke retry", err)
	}
	if _, err := f.s.Retain(ctx, f.p, RetainRequest{o, []ContentRef{a.ContentRef}}); !errors.Is(err, ErrConflict) {
		t.Fatal("owner content changed", err)
	}
	if err := f.s.Delete(ctx, f.p, a.ContentRef, "source project cleanup"); !errors.Is(err, ErrRetained) {
		t.Fatal("protected file deleted", err)
	}
	if err := f.s.Release(ctx, f.p, o); !errors.Is(err, ErrForbidden) {
		t.Fatal("live owner released", err)
	}
	f.policy.release.Store(true)
	if err := f.s.Release(ctx, f.p, o); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Release(ctx, f.p, o); err != nil {
		t.Fatal("release retry failed", err)
	}
	if _, err := f.s.Retain(ctx, f.p, RetainRequest{o, []ContentRef{a.ContentRef, b.ContentRef}}); !errors.Is(err, ErrConflict) {
		t.Fatal("released owner was silently reactivated", err)
	}
	status, err := f.s.Retention(ctx, f.p, o)
	if err != nil || !status.Released || len(status.Refs) != 2 {
		t.Fatal("retention history lost", err)
	}
	if err = f.s.Delete(ctx, f.p, a.ContentRef, "explicit cleanup"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Get(ctx, f.p, a.ContentRef, Access{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("soft deleted file readable", err)
	}
	if _, err = f.s.Get(ctx, f.p, b.ContentRef, Access{}); err != nil {
		t.Fatal("release deleted unrelated version", err)
	}
}

func TestRetainAndDeleteSerialize(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for range 8 {
		v := f.save(t, []byte("race"))
		o := Owner{"project_revision", testID(t), testID(t)}
		start := make(chan struct{})
		kept := make(chan error, 1)
		deleted := make(chan error, 1)
		go func() {
			<-start
			_, err := f.s.Retain(ctx, f.p, RetainRequest{o, []ContentRef{v.ContentRef}})
			kept <- err
		}()
		go func() { <-start; deleted <- f.s.Delete(ctx, f.p, v.ContentRef, "race cleanup") }()
		close(start)
		k, d := <-kept, <-deleted
		if k == nil {
			if !errors.Is(d, ErrRetained) {
				t.Fatal("retained version deletion was not rejected", d)
			}
		} else if d == nil {
			if !errors.Is(k, ErrNotFound) {
				t.Fatal("deleted version retention was not rejected", k)
			}
		} else {
			t.Fatalf("unexpected race result retain=%v delete=%v", k, d)
		}
	}
}

func TestRevocationAndUnavailableAuthorityFailClosed(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := f.request(t, []byte("data"))
	u, err := f.s.Prepare(ctx, f.p, r)
	if err != nil {
		t.Fatal(err)
	}
	f.policy.deny.Store(true)
	if _, err = f.s.Put(ctx, f.p, u.UploadID, bytes.NewReader([]byte("data"))); !errors.Is(err, ErrForbidden) {
		t.Fatal("write continued after revocation", err)
	}
	if _, err = f.s.Commit(ctx, f.p, u.UploadID); !errors.Is(err, ErrForbidden) {
		t.Fatal("commit continued after revocation", err)
	}
	if _, err = New(ctx, f.db, f.blobs, nil, f.store, 1<<20); !errors.Is(err, ErrInvalid) {
		t.Fatal("service accepted missing authorizer", err)
	}
}
