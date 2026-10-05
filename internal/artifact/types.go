package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrInvalid    = errors.New("INVALID_ARGUMENT")
	ErrForbidden  = errors.New("PERMISSION_DENIED")
	ErrNotFound   = errors.New("NOT_FOUND")
	ErrConflict   = errors.New("IDEMPOTENCY_CONFLICT")
	ErrNotReady   = errors.New("CONTENT_NOT_READY")
	ErrRetained   = errors.New("CONTENT_RETAINED")
	ErrDependency = errors.New("DEPENDENCY_UNAVAILABLE")
	idPattern     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	hashPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	tokenPattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	mimePattern   = regexp.MustCompile(`^[^\s;/]+/[^\s;]+(?:;[^\r\n]+)?$`)
)

func ValidID(id string) bool { return idPattern.MatchString(id) }
func newID() (string, error) { id, err := uuid.NewV7(); return id.String(), err }
func validText(s string) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= 256
}
func fingerprint(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type Principal struct {
	OrganizationID string `json:"organization_id"`
	SubjectID      string `json:"subject_id"`
	RequestID      string `json:"request_id"`
}

func (p Principal) Valid() bool {
	return ValidID(p.OrganizationID) && ValidID(p.SubjectID) && ValidID(p.RequestID)
}

type OriginalRef struct {
	ServiceID  string `json:"service_id"`
	ArtifactID string `json:"artifact_id"`
	VersionID  string `json:"version_id"`
	TaskID     string `json:"task_id,omitempty"`
}
type Source struct {
	Kind           string       `json:"kind"`
	ProjectID      string       `json:"project_id,omitempty"`
	ServiceID      string       `json:"service_id,omitempty"`
	RunID          string       `json:"run_id,omitempty"`
	TaskID         string       `json:"task_id,omitempty"`
	Original       *OriginalRef `json:"original_ref,omitempty"`
	AssetID        string       `json:"asset_id,omitempty"`
	AssetVersionID string       `json:"asset_version_id,omitempty"`
}

func (s Source) Valid() bool {
	if s.Kind == "asset_manifest" {
		return ValidID(s.AssetID) && ValidID(s.AssetVersionID) && s.ProjectID == "" && s.ServiceID == "" && s.RunID == "" && s.TaskID == "" && s.Original == nil
	}
	if s.AssetID != "" || s.AssetVersionID != "" {
		return false
	}
	if s.ProjectID != "" && !ValidID(s.ProjectID) {
		return false
	}
	switch s.Kind {
	case "user_edit", "user_import":
		return s.ProjectID != "" && s.ServiceID == "" && s.RunID == "" && s.TaskID == "" && s.Original == nil
	case "task_output":
		if !tokenPattern.MatchString(s.ServiceID) || !validText(s.RunID) || s.Original != nil {
			return false
		}
		if s.TaskID != "" {
			id, err := uuid.Parse(s.TaskID)
			if err != nil || id.String() != s.TaskID {
				return false
			}
		}
		return true
	case "legacy_import":
		return s.ProjectID != "" && s.ServiceID == "" && s.RunID == "" && s.TaskID == "" && s.Original != nil && tokenPattern.MatchString(s.Original.ServiceID) && validText(s.Original.ArtifactID) && validText(s.Original.VersionID) && (s.Original.TaskID == "" || validText(s.Original.TaskID))
	}
	return false
}

// SameOrigin keeps a logical Artifact bound to its original project/run.
func (s Source) SameOrigin(other Source) bool {
	if s.Kind == "asset_manifest" || other.Kind == "asset_manifest" {
		return s.Kind == other.Kind && s.AssetID == other.AssetID && s.AssetVersionID == other.AssetVersionID
	}
	if s.ProjectID != "" && other.ProjectID != "" {
		return s.ProjectID == other.ProjectID
	}
	return s.Kind == "task_output" && other.Kind == "task_output" && s.ServiceID == other.ServiceID && s.RunID == other.RunID
}

type ContentRef struct {
	StoreID    string `json:"store_id"`
	ArtifactID string `json:"artifact_id"`
	VersionID  string `json:"version_id"`
}

func (r ContentRef) Valid() bool {
	return ValidID(r.StoreID) && ValidID(r.ArtifactID) && ValidID(r.VersionID)
}

type Version struct {
	SchemaVersion int `json:"schema_version"`
	ContentRef
	OrganizationID string    `json:"organization_id"`
	SHA256         string    `json:"sha256"`
	Size           int64     `json:"size"`
	MIME           string    `json:"mime"`
	Source         Source    `json:"source"`
	VersionNo      int64     `json:"version_no"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	ObjectID       string    `json:"-"`
}
type PrepareRequest struct {
	WriteID    string `json:"write_id"`
	ArtifactID string `json:"artifact_id,omitempty"`
	Source     Source `json:"source"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	MIME       string `json:"mime"`
}

func (r PrepareRequest) Valid(max int64) bool {
	if !ValidID(r.WriteID) || (r.ArtifactID != "" && !ValidID(r.ArtifactID)) || !r.Source.Valid() || !hashPattern.MatchString(r.SHA256) || r.Size < 0 || r.Size > max || len(r.MIME) > 256 {
		return false
	}
	mt, _, err := mime.ParseMediaType(r.MIME)
	return err == nil && strings.Contains(mt, "/") && mimePattern.MatchString(r.MIME)
}

type Upload struct {
	UploadID       string `json:"upload_id"`
	VersionID      string `json:"version_id"`
	ArtifactID     string `json:"artifact_id"`
	State          string `json:"state"`
	ContentPath    string `json:"content_path"`
	ObjectID       string `json:"-"`
	OrganizationID string `json:"-"`
	CreatedBy      string `json:"-"`
	VersionNo      int64  `json:"-"`
	Source         Source `json:"-"`
	SHA256         string `json:"-"`
	Size           int64  `json:"-"`
	MIME           string `json:"-"`
	RequestHash    string `json:"-"`
}
type Owner struct {
	Kind     string `json:"owner_kind"`
	ID       string `json:"owner_id"`
	CommitID string `json:"commit_id"`
}

func (o Owner) Valid() bool {
	return (o.Kind == "project_revision" || o.Kind == "asset_version" || o.Kind == "pending_commit") && ValidID(o.ID) && ValidID(o.CommitID)
}

type RetainRequest struct {
	Owner
	Refs []ContentRef `json:"refs"`
}
type Retention struct {
	Owner
	Refs     []ContentRef `json:"refs"`
	Released bool         `json:"released"`
}
type Access struct {
	ProjectID      string `json:"project_id,omitempty"`
	RevisionID     string `json:"project_revision_id,omitempty"`
	AssetID        string `json:"asset_id,omitempty"`
	AssetVersionID string `json:"asset_version_id,omitempty"`
}

func (a Access) Valid() bool {
	if a.ProjectID == "" && a.RevisionID == "" && a.AssetID == "" && a.AssetVersionID == "" {
		return true
	}
	return (ValidID(a.ProjectID) && ValidID(a.RevisionID) && a.AssetID == "" && a.AssetVersionID == "") || (ValidID(a.AssetID) && ValidID(a.AssetVersionID) && a.ProjectID == "" && a.RevisionID == "")
}

// Permission is evaluated by the owning business service against live grants.
// For release, that service MUST establish that the owner is terminal/unreachable.
type Permission struct {
	Action  string   `json:"action"`
	Source  *Source  `json:"source,omitempty"`
	Version *Version `json:"version,omitempty"`
	Owner   *Owner   `json:"owner,omitempty"`
	Access  *Access  `json:"access,omitempty"`
}
type Authorizer interface {
	Authorize(context.Context, Principal, Permission) error
}
