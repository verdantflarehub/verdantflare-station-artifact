package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/strictjson"
)

const maxJSON = 4 << 20

type Server struct {
	service *artifact.Service
	token   [32]byte
	mux     *http.ServeMux
	uploads *http.ServeMux
}

func New(service *artifact.Service, token string) (*Server, error) {
	if service == nil || len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("invalid HTTP service configuration")
	}
	s := &Server{service: service, token: sha256.Sum256([]byte("Bearer " + token)), mux: http.NewServeMux(), uploads: http.NewServeMux()}
	s.mux.HandleFunc("/mcp", s.mcp)
	s.mux.HandleFunc("POST /v2/artifacts/uploads", s.prepare)
	s.uploads.HandleFunc("GET /v2/artifacts/uploads/{upload}", s.upload)
	s.uploads.HandleFunc("PUT /v2/artifacts/uploads/{upload}/content", s.put)
	s.uploads.HandleFunc("POST /v2/artifacts/uploads/{upload}/commit", s.commit)
	s.mux.HandleFunc("GET /v2/artifacts/{version}", s.get)
	s.mux.HandleFunc("GET /v2/artifacts/{version}/content", s.content)
	s.mux.HandleFunc("DELETE /v2/artifacts/{version}", s.remove)
	s.mux.HandleFunc("POST /v2/artifacts/retentions", s.retain)
	s.mux.HandleFunc("POST /v2/artifacts/retentions/inspect", s.inspect)
	s.mux.HandleFunc("POST /v2/artifacts/retentions/release", s.release)
	s.mux.HandleFunc("POST /v2/artifacts/legacy/resolve", s.legacy)
	return s, nil
}

func oneHeader(r *http.Request, key string) string {
	v := r.Header.Values(key)
	if len(v) != 1 {
		return ""
	}
	return v[0]
}
func principal(r *http.Request) artifact.Principal {
	return artifact.Principal{OrganizationID: oneHeader(r, "X-Organization-Id"), SubjectID: oneHeader(r, "X-User-Id"), RequestID: oneHeader(r, "X-Request-Id")}
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	p := principal(r)
	actual := sha256.Sum256([]byte(oneHeader(r, "Authorization")))
	if subtle.ConstantTimeCompare(actual[:], s.token[:]) != 1 || !p.Valid() {
		failure(w, r, artifact.ErrForbidden)
		return
	}
	w.Header().Set("X-Request-Id", p.RequestID)
	if r.Header.Get("Content-Encoding") != "" && r.Header.Get("Content-Encoding") != "identity" {
		failure(w, r, artifact.ErrInvalid)
		return
	}
	// Only content lookup endpoints accept query parameters.
	if r.URL.RawQuery != "" && !(r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/v2/artifacts/uploads")) {
		failure(w, r, artifact.ErrInvalid)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v2/artifacts/uploads/") {
		s.uploads.ServeHTTP(w, r)
		return
	}
	s.mux.ServeHTTP(w, r)
}
func output(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func errorStatus(err error) (int, string) {
	status, code := http.StatusInternalServerError, "INTERNAL_ERROR"
	for _, item := range []struct {
		err    error
		status int
	}{
		{artifact.ErrInvalid, 400}, {artifact.ErrForbidden, 403}, {artifact.ErrNotFound, 404},
		{artifact.ErrConflict, 409}, {artifact.ErrNotReady, 409}, {artifact.ErrRetained, 409}, {artifact.ErrDependency, 503},
	} {
		if errors.Is(err, item.err) {
			status, code = item.status, item.err.Error()
			break
		}
	}
	return status, code
}
func failure(w http.ResponseWriter, r *http.Request, err error) {
	status, code := errorStatus(err)
	id := oneHeader(r, "X-Request-Id")
	if !artifact.ValidID(id) {
		generated, e := uuid.NewV7()
		if e == nil {
			id = generated.String()
		} else {
			id = ""
		}
	}
	w.Header().Set("X-Request-Id", id)
	output(w, status, struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}{code, "Artifact request failed", id})
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	mt, _, err := mime.ParseMediaType(oneHeader(r, "Content-Type"))
	if err != nil || mt != "application/json" || strictjson.Decode(r.Body, maxJSON, dst) != nil {
		failure(w, r, artifact.ErrInvalid)
		return false
	}
	return true
}
func empty(w http.ResponseWriter, r *http.Request) bool {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(b) != 0 {
		failure(w, r, artifact.ErrInvalid)
		return false
	}
	return true
}
func result(w http.ResponseWriter, r *http.Request, value any, err error) {
	if err != nil {
		failure(w, r, err)
		return
	}
	output(w, http.StatusOK, value)
}
func (s *Server) prepare(w http.ResponseWriter, r *http.Request) {
	var req artifact.PrepareRequest
	if !decode(w, r, &req) {
		return
	}
	u, err := s.service.Prepare(r.Context(), principal(r), req)
	result(w, r, u, err)
}
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if !empty(w, r) {
		return
	}
	u, err := s.service.Upload(r.Context(), principal(r), r.PathValue("upload"))
	result(w, r, u, err)
}
func (s *Server) put(w http.ResponseWriter, r *http.Request) {
	u, err := s.service.Put(r.Context(), principal(r), r.PathValue("upload"), r.Body)
	result(w, r, u, err)
}
func (s *Server) commit(w http.ResponseWriter, r *http.Request) {
	if !empty(w, r) {
		return
	}
	v, err := s.service.Commit(r.Context(), principal(r), r.PathValue("upload"))
	result(w, r, v, err)
}
func query(r *http.Request) (artifact.ContentRef, artifact.Access, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	allowed := map[string]bool{"store_id": true, "artifact_id": true, "project_id": true, "project_revision_id": true, "asset_id": true, "asset_version_id": true}
	if err != nil {
		return artifact.ContentRef{}, artifact.Access{}, artifact.ErrInvalid
	}
	for k, v := range q {
		if !allowed[k] || len(v) != 1 || v[0] == "" {
			return artifact.ContentRef{}, artifact.Access{}, artifact.ErrInvalid
		}
	}
	ref := artifact.ContentRef{StoreID: q.Get("store_id"), ArtifactID: q.Get("artifact_id"), VersionID: r.PathValue("version")}
	a := artifact.Access{ProjectID: q.Get("project_id"), RevisionID: q.Get("project_revision_id"), AssetID: q.Get("asset_id"), AssetVersionID: q.Get("asset_version_id")}
	if !ref.Valid() || !a.Valid() {
		return ref, a, artifact.ErrInvalid
	}
	return ref, a, nil
}
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	if !empty(w, r) {
		return
	}
	ref, a, err := query(r)
	if err != nil {
		failure(w, r, err)
		return
	}
	v, err := s.service.Get(r.Context(), principal(r), ref, a)
	result(w, r, v, err)
}
func (s *Server) content(w http.ResponseWriter, r *http.Request) {
	if !empty(w, r) {
		return
	}
	ref, a, err := query(r)
	if err != nil {
		failure(w, r, err)
		return
	}
	v, f, err := s.service.Open(r.Context(), principal(r), ref, a)
	if err != nil {
		failure(w, r, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", v.MIME)
	w.Header().Set("Content-Disposition", `attachment; filename="`+v.VersionID+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(v.Size, 10))
	w.Header().Set("ETag", `"sha256-`+v.SHA256+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}
func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StoreID    string `json:"store_id"`
		ArtifactID string `json:"artifact_id"`
		Reason     string `json:"reason"`
	}
	if !decode(w, r, &req) {
		return
	}
	err := s.service.Delete(r.Context(), principal(r), artifact.ContentRef{StoreID: req.StoreID, ArtifactID: req.ArtifactID, VersionID: r.PathValue("version")}, req.Reason)
	if err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) retain(w http.ResponseWriter, r *http.Request) {
	var req artifact.RetainRequest
	if !decode(w, r, &req) {
		return
	}
	value, err := s.service.Retain(r.Context(), principal(r), req)
	result(w, r, value, err)
}
func (s *Server) inspect(w http.ResponseWriter, r *http.Request) {
	var req artifact.Owner
	if !decode(w, r, &req) {
		return
	}
	value, err := s.service.Retention(r.Context(), principal(r), req)
	result(w, r, value, err)
}
func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	var req artifact.Owner
	if !decode(w, r, &req) {
		return
	}
	if err := s.service.Release(r.Context(), principal(r), req); err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) legacy(w http.ResponseWriter, r *http.Request) {
	var req artifact.Source
	if !decode(w, r, &req) {
		return
	}
	v, err := s.service.ResolveLegacy(r.Context(), principal(r), req)
	result(w, r, v, err)
}
