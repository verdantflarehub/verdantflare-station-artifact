package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/authority"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/storage"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/testdb"
	"github.com/verdantflarehub/verdantflare-station-artifact/migrations"
)

func id() string { return uuid.Must(uuid.NewV7()).String() }

func TestHTTPPersistentContentAndRetention(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	store, project := id(), id()
	p := artifact.Principal{OrganizationID: id(), SubjectID: id(), RequestID: id()}
	if err := migrations.Apply(ctx, db, store); err != nil {
		t.Fatal(err)
	}
	blobs, err := storage.NewLocal(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Close()
	token, authToken := strings.Repeat("s", 32), strings.Repeat("a", 32)
	var revoked, released atomic.Bool
	var decisions atomic.Int32
	var evidenceCount atomic.Int32
	evidence := func(schema string, data []byte) {
		if dir := os.Getenv("ARTIFACT_CONTRACT_EVIDENCE_DIR"); dir != "" {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Error(err)
				return
			}
			name := fmt.Sprintf("%03d.%s.json", evidenceCount.Add(1), schema)
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				t.Error(err)
			}
		}
	}
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decisions.Add(1)
		var req struct {
			Principal  artifact.Principal  `json:"principal"`
			Permission artifact.Permission `json:"permission"`
		}
		payload, readErr := io.ReadAll(r.Body)
		if readErr != nil || json.Unmarshal(payload, &req) != nil {
			t.Error("bad authority request")
		}
		evidence("artifact-authorization", payload)
		allowed := r.Header.Get("Authorization") == "Bearer "+authToken && req.Principal == p && !revoked.Load()
		if req.Permission.Action == "write" {
			allowed = allowed && req.Permission.Source != nil && req.Permission.Source.ProjectID == project
		}
		if req.Permission.Action == "release" {
			allowed = allowed && released.Load()
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"allowed": allowed})
	}))
	defer policy.Close()
	auth, err := authority.New(policy.URL, authToken)
	if err != nil {
		t.Fatal(err)
	}
	service, err := artifact.New(ctx, db, blobs, auth, store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(service, token)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	request := func(method, path string, body []byte, modify func(*http.Request), want int) []byte {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-User-Id", p.SubjectID)
		r.Header.Set("X-Organization-Id", p.OrganizationID)
		r.Header.Set("X-Request-Id", p.RequestID)
		r.Header.Set("Content-Type", "application/json")
		if modify != nil {
			modify(r)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, response.StatusCode, want, data)
		}
		if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing protected response headers")
		}
		if method == "GET" && strings.Contains(path, "/content?") && want == 200 && !strings.HasPrefix(response.Header.Get("Content-Disposition"), "attachment;") {
			t.Fatal("active content rendered inline")
		}
		if want >= 400 && (strings.Contains(string(data), "station.") || strings.Contains(string(data), "password") || strings.Contains(string(data), "objects/")) {
			t.Fatal("internal error exposed")
		}
		if want >= 400 {
			evidence("artifact-error", data)
		}
		if want == 200 {
			switch {
			case strings.Contains(path, "/retentions"):
				evidence("artifact-retention", data)
			case strings.Contains(path, "/uploads") && !strings.HasSuffix(path, "/commit"):
				evidence("artifact-upload", data)
			case !strings.Contains(path, "/content?"):
				evidence("artifact-version", data)
			}
			if method == "POST" && path == "/v2/artifacts/uploads" {
				evidence("artifact-upload-request", body)
			}
			if method == "POST" && path == "/v2/artifacts/retentions" {
				evidence("artifact-retain-request", body)
			}
		}
		return data
	}
	marshal := func(v any) []byte {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	data := []byte("<h1>MD and media are user content</h1>")
	hash := sha256.Sum256(data)
	req := artifact.PrepareRequest{WriteID: id(), Source: artifact.Source{Kind: "user_edit", ProjectID: project}, SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data)), MIME: "text/html"}
	valid := marshal(req)
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del("Authorization") },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer invalid") },
		func(r *http.Request) { r.Header.Add("X-User-Id", id()) },
		func(r *http.Request) { r.Header.Set("X-Request-Id", "not-an-id") },
	} {
		request("POST", "/v2/artifacts/uploads", valid, mutate, 403)
	}
	if decisions.Load() != 0 {
		t.Fatal("untrusted requests reached authority")
	}
	for _, invalid := range []string{
		strings.Replace(string(valid), `"size":`, `"Size":`, 1),
		strings.Replace(string(valid), `"size":`, `"size":0,"size":`, 1),
		strings.Replace(string(valid), `"kind":"user_edit"`, `"kind":"user_edit","run_id":null`, 1),
		strings.Replace(string(valid), `"kind":"user_edit"`, `"kind":"user_edit","run_id":""`, 1),
		strings.Replace(string(valid), `"size":`+strconvSize(req.Size)+`,`, ``, 1),
		string(valid) + ` {}`,
	} {
		request("POST", "/v2/artifacts/uploads", []byte(invalid), nil, 400)
	}
	if decisions.Load() != 0 {
		t.Fatal("malformed requests reached authority")
	}
	var u artifact.Upload
	if err := json.Unmarshal(request("POST", "/v2/artifacts/uploads", valid, nil, 200), &u); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(request("GET", "/v2/artifacts/uploads/"+u.UploadID, nil, nil, 200)), "object_id") {
		t.Fatal("internal storage ID exposed")
	}
	request("POST", "/v2/artifacts/uploads/"+u.UploadID+"/commit", nil, nil, 409)
	request("PUT", u.ContentPath, []byte("wrong"), nil, 400)
	request("PUT", u.ContentPath, data, nil, 200)
	var v artifact.Version
	if err := json.Unmarshal(request("POST", "/v2/artifacts/uploads/"+u.UploadID+"/commit", nil, nil, 200), &v); err != nil {
		t.Fatal(err)
	}
	path := "/v2/artifacts/" + v.VersionID
	q := "?store_id=" + store + "&artifact_id=" + v.ArtifactID
	request("GET", path+q+"&store_id="+store, nil, nil, 400)
	request("GET", path+q+"&asset_id="+id(), nil, nil, 400)
	request("GET", path+q+"&unknown=x", nil, nil, 400)
	request("GET", path+"?store_id="+id()+"&artifact_id="+v.ArtifactID, nil, nil, 404)
	if got := request("GET", path+"/content"+q, nil, nil, 200); !bytes.Equal(got, data) {
		t.Fatal("download changed content")
	}
	revoked.Store(true)
	request("GET", path+q, nil, nil, 403)
	request("POST", "/v2/artifacts/uploads/"+u.UploadID+"/commit", nil, nil, 403)
	revoked.Store(false)
	owner := artifact.Owner{Kind: "project_revision", ID: id(), CommitID: id()}
	retention := artifact.RetainRequest{Owner: owner, Refs: []artifact.ContentRef{v.ContentRef}}
	request("POST", "/v2/artifacts/retentions", marshal(retention), nil, 200)
	request("POST", "/v2/artifacts/retentions/inspect", marshal(owner), nil, 200)
	deletion := marshal(map[string]string{"store_id": store, "artifact_id": v.ArtifactID, "reason": "synthetic fixture"})
	request("DELETE", path, deletion, nil, 409)
	request("POST", "/v2/artifacts/retentions/release", marshal(owner), nil, 403)
	released.Store(true)
	request("POST", "/v2/artifacts/retentions/release", marshal(owner), nil, 204)
	request("DELETE", path, deletion, nil, 204)
	request("GET", path+q, nil, nil, 404)
	policy.Close()
	req.WriteID = id()
	request("POST", "/v2/artifacts/uploads", marshal(req), nil, 503)
}

func strconvSize(n int64) string { b, _ := json.Marshal(n); return string(b) }
