package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/storage"
	"github.com/verdantflarehub/verdantflare-station-artifact/migrations"
)

type Service struct {
	db      *pgxpool.Pool
	blobs   *storage.Local
	auth    Authorizer
	storeID string
	maxSize int64
}

func New(ctx context.Context, db *pgxpool.Pool, blobs *storage.Local, auth Authorizer, storeID string, maxSize int64) (*Service, error) {
	if db == nil || blobs == nil || auth == nil || !ValidID(storeID) || maxSize <= 0 || maxSize > 1<<50 {
		return nil, ErrInvalid
	}
	if err := migrations.Check(ctx, db, storeID); err != nil {
		return nil, err
	}
	return &Service{db, blobs, auth, storeID, maxSize}, nil
}
func (s *Service) Ready(ctx context.Context) error { return migrations.Check(ctx, s.db, s.storeID) }
func (s *Service) authorize(ctx context.Context, p Principal, permission Permission) error {
	if !p.Valid() {
		return ErrForbidden
	}
	return s.auth.Authorize(ctx, p, permission)
}
func rollback(tx pgx.Tx) { _ = tx.Rollback(context.Background()) }
func lockKey(ctx context.Context, tx pgx.Tx, key string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", key)
	return err
}
func audit(ctx context.Context, tx pgx.Tx, p Principal, action, resource string) error {
	id, err := newID()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO station.artifact_events(event_id,organization_id,subject_id,action,resource_id,request_id) VALUES($1,$2,$3,$4,$5,$6)", id, p.OrganizationID, p.SubjectID, action, resource, p.RequestID)
	return err
}

const uploadColumns = `upload_id::text,version_id::text,artifact_id::text,state,object_id::text,organization_id::text,created_by::text,version_no,source,sha256,size,mime,request_sha256`

func scanUpload(row pgx.Row) (Upload, error) {
	var u Upload
	var source []byte
	err := row.Scan(&u.UploadID, &u.VersionID, &u.ArtifactID, &u.State, &u.ObjectID, &u.OrganizationID, &u.CreatedBy, &u.VersionNo, &source, &u.SHA256, &u.Size, &u.MIME, &u.RequestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	if err = json.Unmarshal(source, &u.Source); err != nil {
		return u, err
	}
	u.ContentPath = "/v2/artifacts/uploads/" + u.UploadID + "/content"
	return u, nil
}

func (s *Service) Prepare(ctx context.Context, p Principal, r PrepareRequest) (Upload, error) {
	if !r.Valid(s.maxSize) {
		return Upload{}, ErrInvalid
	}
	if err := s.authorize(ctx, p, Permission{Action: "write", Source: &r.Source}); err != nil {
		return Upload{}, err
	}
	digest, err := fingerprint(r)
	if err != nil {
		return Upload{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Upload{}, err
	}
	defer rollback(tx)
	if err = lockKey(ctx, tx, "upload:"+p.OrganizationID+":"+p.SubjectID+":"+r.WriteID); err != nil {
		return Upload{}, err
	}
	u, err := scanUpload(tx.QueryRow(ctx, "SELECT "+uploadColumns+" FROM station.artifact_uploads WHERE organization_id=$1 AND created_by=$2 AND write_id=$3", p.OrganizationID, p.SubjectID, r.WriteID))
	if err == nil {
		if u.RequestHash != digest {
			return Upload{}, ErrConflict
		}
		return u, tx.Commit(ctx)
	}
	if !errors.Is(err, ErrNotFound) {
		return Upload{}, err
	}
	artifactID := r.ArtifactID
	if artifactID == "" {
		artifactID, err = newID()
		if err != nil {
			return Upload{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO station.artifact_artifacts(artifact_id,organization_id,created_by) VALUES($1,$2,$3)", artifactID, p.OrganizationID, p.SubjectID); err != nil {
			return Upload{}, err
		}
	} else {
		// An artifact cannot be moved to a different origin to bypass authorization.
		var original []byte
		err = tx.QueryRow(ctx, "SELECT source FROM station.artifact_uploads WHERE organization_id=$1 AND artifact_id=$2 ORDER BY version_no LIMIT 1", p.OrganizationID, artifactID).Scan(&original)
		if errors.Is(err, pgx.ErrNoRows) {
			return Upload{}, ErrNotFound
		}
		if err != nil {
			return Upload{}, err
		}
		var source Source
		if err = json.Unmarshal(original, &source); err != nil {
			return Upload{}, err
		}
		if !source.SameOrigin(r.Source) {
			return Upload{}, ErrForbidden
		}
	}
	ids := make([]string, 3)
	for i := range ids {
		ids[i], err = newID()
		if err != nil {
			return Upload{}, err
		}
	}
	var versionNo int64
	err = tx.QueryRow(ctx, "UPDATE station.artifact_artifacts SET next_version_no=next_version_no+1 WHERE organization_id=$1 AND artifact_id=$2 RETURNING next_version_no-1", p.OrganizationID, artifactID).Scan(&versionNo)
	if err != nil {
		return Upload{}, err
	}
	source, err := json.Marshal(r.Source)
	if err != nil {
		return Upload{}, err
	}
	u, err = scanUpload(tx.QueryRow(ctx, `INSERT INTO station.artifact_uploads(upload_id,organization_id,created_by,write_id,request_sha256,artifact_id,version_id,version_no,object_id,source,sha256,size,mime)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING `+uploadColumns, ids[0], p.OrganizationID, p.SubjectID, r.WriteID, digest, artifactID, ids[1], versionNo, ids[2], source, r.SHA256, r.Size, r.MIME))
	if err != nil {
		return Upload{}, err
	}
	if err = audit(ctx, tx, p, "upload.prepare", u.UploadID); err != nil {
		return Upload{}, err
	}
	return u, tx.Commit(ctx)
}

func (s *Service) Upload(ctx context.Context, p Principal, id string) (Upload, error) {
	if !p.Valid() || !ValidID(id) {
		return Upload{}, ErrInvalid
	}
	u, err := scanUpload(s.db.QueryRow(ctx, "SELECT "+uploadColumns+" FROM station.artifact_uploads WHERE organization_id=$1 AND created_by=$2 AND upload_id=$3", p.OrganizationID, p.SubjectID, id))
	if err != nil {
		return Upload{}, err
	}
	if err = s.authorize(ctx, p, Permission{Action: "write", Source: &u.Source}); err != nil {
		return Upload{}, err
	}
	return u, nil
}

func (s *Service) Put(ctx context.Context, p Principal, id string, r io.Reader) (Upload, error) {
	u, err := s.Upload(ctx, p, id)
	if err != nil {
		return Upload{}, err
	}
	_, err = s.blobs.Put(ctx, storage.Object{ID: u.ObjectID, SHA256: u.SHA256, Size: u.Size}, r)
	if errors.Is(err, storage.ErrMismatch) || errors.Is(err, storage.ErrInvalid) {
		return Upload{}, ErrInvalid
	}
	if errors.Is(err, storage.ErrConflict) {
		return Upload{}, ErrConflict
	}
	if err != nil {
		return Upload{}, err
	}
	return u, nil
}

func (s *Service) Commit(ctx context.Context, p Principal, id string) (Version, error) {
	u, err := s.Upload(ctx, p, id)
	if err != nil {
		return Version{}, err
	}
	file, err := s.blobs.Open(ctx, storage.Object{ID: u.ObjectID, SHA256: u.SHA256, Size: u.Size})
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, storage.ErrCorrupt) {
		return Version{}, ErrNotReady
	}
	if err != nil {
		return Version{}, err
	}
	file.Close()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Version{}, err
	}
	defer rollback(tx)
	u, err = scanUpload(tx.QueryRow(ctx, "SELECT "+uploadColumns+" FROM station.artifact_uploads WHERE organization_id=$1 AND created_by=$2 AND upload_id=$3 FOR UPDATE", p.OrganizationID, p.SubjectID, id))
	if err != nil {
		return Version{}, err
	}
	if u.State == "committed" {
		v, err := s.version(ctx, tx, p.OrganizationID, u.VersionID)
		if err != nil {
			return Version{}, err
		}
		return v, tx.Commit(ctx)
	}
	if u.Source.Kind == "legacy_import" {
		original := u.Source.Original
		key := struct {
			Org, Service, Artifact, Version string
		}{p.OrganizationID, original.ServiceID, original.ArtifactID, original.VersionID}
		hash, _ := fingerprint(key)
		if err = lockKey(ctx, tx, "legacy:"+hash); err != nil {
			return Version{}, err
		}
		var prior string
		err = tx.QueryRow(ctx, "SELECT version_id::text FROM station.artifact_legacy_refs WHERE organization_id=$1 AND service_id=$2 AND legacy_artifact_id=$3 AND legacy_version_id=$4", p.OrganizationID, original.ServiceID, original.ArtifactID, original.VersionID).Scan(&prior)
		if err == nil {
			return Version{}, ErrConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Version{}, err
		}
	}
	_, err = tx.Exec(ctx, "INSERT INTO station.artifact_objects(object_id,organization_id,storage_key,sha256,size) VALUES($1,$2,$3,$4,$5)", u.ObjectID, p.OrganizationID, "objects/"+u.ObjectID, u.SHA256, u.Size)
	if err != nil {
		return Version{}, err
	}
	source, _ := json.Marshal(u.Source)
	_, err = tx.Exec(ctx, `INSERT INTO station.artifact_versions(version_id,artifact_id,organization_id,version_no,object_id,source,sha256,size,mime,created_by)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, u.VersionID, u.ArtifactID, p.OrganizationID, u.VersionNo, u.ObjectID, source, u.SHA256, u.Size, u.MIME, p.SubjectID)
	if err != nil {
		return Version{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO station.artifact_version_lifecycle(version_id) VALUES($1)", u.VersionID); err != nil {
		return Version{}, err
	}
	if u.Source.Kind == "legacy_import" {
		o := u.Source.Original
		_, err = tx.Exec(ctx, "INSERT INTO station.artifact_legacy_refs(organization_id,service_id,legacy_artifact_id,legacy_version_id,version_id) VALUES($1,$2,$3,$4,$5)", p.OrganizationID, o.ServiceID, o.ArtifactID, o.VersionID, u.VersionID)
		if err != nil {
			return Version{}, err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE station.artifact_uploads SET state='committed',committed_at=now() WHERE upload_id=$1", u.UploadID); err != nil {
		return Version{}, err
	}
	if err = audit(ctx, tx, p, "upload.commit", u.VersionID); err != nil {
		return Version{}, err
	}
	v, err := s.version(ctx, tx, p.OrganizationID, u.VersionID)
	if err != nil {
		return Version{}, err
	}
	return v, tx.Commit(ctx)
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// ResolveLegacy reuses an existing immutable mapping after checking both the
// import destination and the mapped content. It never fetches a client URL.
func (s *Service) ResolveLegacy(ctx context.Context, p Principal, source Source) (Version, error) {
	if source.Kind != "legacy_import" || !source.Valid() {
		return Version{}, ErrInvalid
	}
	if err := s.authorize(ctx, p, Permission{Action: "write", Source: &source}); err != nil {
		return Version{}, err
	}
	o := source.Original
	var id string
	err := s.db.QueryRow(ctx, "SELECT version_id::text FROM station.artifact_legacy_refs WHERE organization_id=$1 AND service_id=$2 AND legacy_artifact_id=$3 AND legacy_version_id=$4", p.OrganizationID, o.ServiceID, o.ArtifactID, o.VersionID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	if err != nil {
		return Version{}, err
	}
	v, err := s.version(ctx, s.db, p.OrganizationID, id)
	if err != nil {
		return Version{}, err
	}
	return s.Get(ctx, p, v.ContentRef, Access{})
}

func (s *Service) version(ctx context.Context, q querier, org, id string) (Version, error) {
	var v Version
	var source []byte
	err := q.QueryRow(ctx, `SELECT v.version_id::text,v.artifact_id::text,v.organization_id::text,v.version_no,v.object_id::text,v.source,v.sha256,v.size,v.mime,v.created_by::text,v.created_at
FROM station.artifact_versions v JOIN station.artifact_version_lifecycle l USING(version_id) WHERE v.organization_id=$1 AND v.version_id=$2 AND l.deleted_at IS NULL`, org, id).Scan(&v.VersionID, &v.ArtifactID, &v.OrganizationID, &v.VersionNo, &v.ObjectID, &source, &v.SHA256, &v.Size, &v.MIME, &v.CreatedBy, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal(source, &v.Source); err != nil {
		return v, err
	}
	v.StoreID = s.storeID
	v.SchemaVersion = 2
	v.CreatedAt = v.CreatedAt.UTC()
	return v, nil
}
func (s *Service) resolve(ctx context.Context, p Principal, ref ContentRef, access Access, action string) (Version, error) {
	if !p.Valid() || !ref.Valid() || !access.Valid() {
		return Version{}, ErrInvalid
	}
	if ref.StoreID != s.storeID {
		return Version{}, ErrNotFound
	}
	v, err := s.version(ctx, s.db, p.OrganizationID, ref.VersionID)
	if err != nil {
		return Version{}, err
	}
	if ref.ArtifactID != v.ArtifactID {
		return Version{}, ErrNotFound
	}
	if err = s.authorize(ctx, p, Permission{Action: action, Version: &v, Access: &access}); err != nil {
		return Version{}, err
	}
	return v, nil
}
func (s *Service) Get(ctx context.Context, p Principal, ref ContentRef, access Access) (Version, error) {
	v, err := s.resolve(ctx, p, ref, access, "read")
	if err != nil {
		return v, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Version{}, err
	}
	defer rollback(tx)
	if err = audit(ctx, tx, p, "version.read", v.VersionID); err != nil {
		return Version{}, err
	}
	return v, tx.Commit(ctx)
}
func (s *Service) Open(ctx context.Context, p Principal, ref ContentRef, access Access) (Version, *os.File, error) {
	v, err := s.Get(ctx, p, ref, access)
	if err != nil {
		return Version{}, nil, err
	}
	f, err := s.blobs.Open(ctx, storage.Object{ID: v.ObjectID, SHA256: v.SHA256, Size: v.Size})
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, storage.ErrCorrupt) {
		return Version{}, nil, ErrNotReady
	}
	return v, f, err
}

// lockVersion is used by both retain and delete; ordered acquisition prevents
// partially retained bundles and closes the check-then-delete race.
func lockVersion(ctx context.Context, tx pgx.Tx, org, id string) error {
	var deleted bool
	err := tx.QueryRow(ctx, `SELECT l.deleted_at IS NOT NULL FROM station.artifact_version_lifecycle l JOIN station.artifact_versions v USING(version_id)
WHERE v.organization_id=$1 AND l.version_id=$2 FOR UPDATE OF l`, org, id).Scan(&deleted)
	if errors.Is(err, pgx.ErrNoRows) || deleted {
		return ErrNotFound
	}
	return err
}
func canonicalRefs(refs []ContentRef) ([]ContentRef, error) {
	if len(refs) == 0 || len(refs) > 10001 {
		return nil, ErrInvalid
	}
	refs = append([]ContentRef{}, refs...)
	sort.Slice(refs, func(i, j int) bool { return refs[i].VersionID < refs[j].VersionID })
	for i, r := range refs {
		if !r.Valid() || (i > 0 && r.VersionID == refs[i-1].VersionID) {
			return nil, ErrInvalid
		}
	}
	return refs, nil
}

func (s *Service) Retain(ctx context.Context, p Principal, r RetainRequest) (Retention, error) {
	if !r.Owner.Valid() {
		return Retention{}, ErrInvalid
	}
	refs, err := canonicalRefs(r.Refs)
	if err != nil {
		return Retention{}, err
	}
	r.Refs = refs
	if err = s.authorize(ctx, p, Permission{Action: "retain", Owner: &r.Owner}); err != nil {
		return Retention{}, err
	}
	for _, ref := range refs {
		if ref.StoreID != s.storeID {
			return Retention{}, ErrNotFound
		}
		v, err := s.version(ctx, s.db, p.OrganizationID, ref.VersionID)
		if err != nil {
			return Retention{}, err
		}
		if ref.ArtifactID != v.ArtifactID {
			return Retention{}, ErrNotFound
		}
		if err = s.authorize(ctx, p, Permission{Action: "retain_content", Owner: &r.Owner, Version: &v}); err != nil {
			return Retention{}, err
		}
		// Retention is the last content-readiness check before a business service
		// publishes a revision. Metadata alone cannot prove the bytes survived.
		file, err := s.blobs.Open(ctx, storage.Object{ID: v.ObjectID, SHA256: v.SHA256, Size: v.Size})
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, storage.ErrCorrupt) {
			return Retention{}, ErrNotReady
		}
		if err != nil {
			return Retention{}, err
		}
		if err = file.Close(); err != nil {
			return Retention{}, err
		}
	}
	digest, err := fingerprint(r)
	if err != nil {
		return Retention{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Retention{}, err
	}
	defer rollback(tx)
	if err = lockKey(ctx, tx, "owner:"+p.OrganizationID+":"+r.Kind+":"+r.ID); err != nil {
		return Retention{}, err
	}
	var prior string
	var released bool
	err = tx.QueryRow(ctx, "SELECT request_sha256,released_at IS NOT NULL FROM station.artifact_retention_owners WHERE organization_id=$1 AND owner_kind=$2 AND owner_id=$3", p.OrganizationID, r.Kind, r.ID).Scan(&prior, &released)
	if err == nil {
		if prior != digest || released {
			return Retention{}, ErrConflict
		}
		return Retention{Owner: r.Owner, Refs: refs}, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Retention{}, err
	}
	for _, ref := range refs {
		if err = lockVersion(ctx, tx, p.OrganizationID, ref.VersionID); err != nil {
			return Retention{}, err
		}
	}
	_, err = tx.Exec(ctx, "INSERT INTO station.artifact_retention_owners(organization_id,owner_kind,owner_id,commit_id,request_sha256) VALUES($1,$2,$3,$4,$5)", p.OrganizationID, r.Kind, r.ID, r.CommitID, digest)
	if err != nil {
		return Retention{}, err
	}
	for _, ref := range refs {
		_, err = tx.Exec(ctx, "INSERT INTO station.artifact_retention_refs(organization_id,owner_kind,owner_id,version_id) VALUES($1,$2,$3,$4)", p.OrganizationID, r.Kind, r.ID, ref.VersionID)
		if err != nil {
			return Retention{}, err
		}
	}
	if err = audit(ctx, tx, p, "retention.create", r.ID); err != nil {
		return Retention{}, err
	}
	return Retention{Owner: r.Owner, Refs: refs}, tx.Commit(ctx)
}

func (s *Service) Retention(ctx context.Context, p Principal, o Owner) (Retention, error) {
	if !o.Valid() {
		return Retention{}, ErrInvalid
	}
	if err := s.authorize(ctx, p, Permission{Action: "inspect_retention", Owner: &o}); err != nil {
		return Retention{}, err
	}
	var result Retention
	result.Owner = o
	result.Refs = []ContentRef{}
	err := s.db.QueryRow(ctx, "SELECT released_at IS NOT NULL FROM station.artifact_retention_owners WHERE organization_id=$1 AND owner_kind=$2 AND owner_id=$3 AND commit_id=$4", p.OrganizationID, o.Kind, o.ID, o.CommitID).Scan(&result.Released)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	rows, err := s.db.Query(ctx, `SELECT v.artifact_id::text,v.version_id::text FROM station.artifact_retention_refs r JOIN station.artifact_versions v USING(organization_id,version_id)
WHERE r.organization_id=$1 AND r.owner_kind=$2 AND r.owner_id=$3 ORDER BY v.version_id`, p.OrganizationID, o.Kind, o.ID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		ref := ContentRef{StoreID: s.storeID}
		if err = rows.Scan(&ref.ArtifactID, &ref.VersionID); err != nil {
			return result, err
		}
		result.Refs = append(result.Refs, ref)
	}
	return result, rows.Err()
}
func (s *Service) Release(ctx context.Context, p Principal, o Owner) error {
	if !o.Valid() {
		return ErrInvalid
	}
	if err := s.authorize(ctx, p, Permission{Action: "release", Owner: &o}); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = lockKey(ctx, tx, "owner:"+p.OrganizationID+":"+o.Kind+":"+o.ID); err != nil {
		return err
	}
	var commit string
	var released bool
	err = tx.QueryRow(ctx, "SELECT commit_id::text,released_at IS NOT NULL FROM station.artifact_retention_owners WHERE organization_id=$1 AND owner_kind=$2 AND owner_id=$3", p.OrganizationID, o.Kind, o.ID).Scan(&commit, &released)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if commit != o.CommitID {
		return ErrConflict
	}
	if !released {
		if _, err = tx.Exec(ctx, "UPDATE station.artifact_retention_owners SET released_at=now() WHERE organization_id=$1 AND owner_kind=$2 AND owner_id=$3", p.OrganizationID, o.Kind, o.ID); err != nil {
			return err
		}
		if err = audit(ctx, tx, p, "retention.release", o.ID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *Service) Delete(ctx context.Context, p Principal, ref ContentRef, reason string) error {
	if !validText(reason) {
		return ErrInvalid
	}
	v, err := s.resolve(ctx, p, ref, Access{}, "delete")
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = lockVersion(ctx, tx, p.OrganizationID, v.VersionID); err != nil {
		return err
	}
	var retained bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM station.artifact_retention_refs r JOIN station.artifact_retention_owners o USING(organization_id,owner_kind,owner_id)
WHERE r.organization_id=$1 AND r.version_id=$2 AND o.released_at IS NULL)`, p.OrganizationID, v.VersionID).Scan(&retained)
	if err != nil {
		return err
	}
	if retained {
		return ErrRetained
	}
	_, err = tx.Exec(ctx, "UPDATE station.artifact_version_lifecycle SET deleted_at=now(),deleted_by=$1,delete_reason=$2 WHERE version_id=$3", p.SubjectID, reason, v.VersionID)
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, p, "version.delete", v.VersionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
