package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/storage"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/testdb"
	"github.com/verdantflarehub/verdantflare-station-artifact/migrations"
)

func TestMCPRejectsInvalidWritesBeforeStorage(t *testing.T) {
	s := &Server{service: &artifact.Service{}}
	p := artifact.Principal{OrganizationID: id(), SubjectID: id(), RequestID: id()}
	valid := map[string]any{"mode": "text", "write_id": id(), "source": map[string]string{"kind": "user_edit", "project_id": id()}, "mime": "text/plain", "text": "hello"}
	for _, change := range []map[string]any{
		{"mode": "unknown"}, {"text": nil}, {"mime": "application/octet-stream"}, {"mime": "text/plain; charset=gbk"},
		{"text": strings.Repeat("中", (1<<20)/3+1)}, {"upload_id": id()},
		{"source": map[string]string{"kind": "task_output", "service_id": "image", "run_id": "native"}},
		{"source": map[string]string{"kind": "asset_manifest", "asset_id": id(), "asset_version_id": id()}},
	} {
		in := map[string]any{}
		for k, v := range valid {
			in[k] = v
		}
		for k, v := range change {
			in[k] = v
		}
		b, _ := json.Marshal(in)
		if _, err := s.writeMCP(context.Background(), p, b); !errors.Is(err, artifact.ErrInvalid) {
			t.Fatalf("invalid input reached storage: %v", err)
		}
	}
}

func TestMCPDeleteRejectsInvalidInputBeforeStorage(t *testing.T) {
	s := &Server{service: &artifact.Service{}}
	p := artifact.Principal{OrganizationID: id(), SubjectID: id(), RequestID: id()}
	ref := artifact.ContentRef{StoreID: id(), ArtifactID: id(), VersionID: id()}
	for _, in := range []map[string]any{
		{"content_ref": ref}, {"content_ref": ref, "reason": "  "},
		{"content_ref": ref, "reason": strings.Repeat("x", 257)},
		{"content_ref": ref, "reason": "cleanup", "project_id": id()},
		{"content_ref": map[string]string{"store_id": id(), "artifact_id": id()}, "reason": "cleanup"},
	} {
		data, _ := json.Marshal(in)
		if _, err := s.deleteMCP(context.Background(), p, data); !errors.Is(err, artifact.ErrInvalid) {
			t.Fatalf("invalid delete reached storage: %v", err)
		}
	}
}

func TestMCPDeleteFixedVersion(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	storeID := id()
	if err := migrations.Apply(ctx, db, storeID); err != nil {
		t.Fatal(err)
	}
	blobs, err := storage.NewLocal(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Close()
	service, err := artifact.New(ctx, db, blobs, storeID, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	p := artifact.Principal{OrganizationID: id(), SubjectID: id(), RequestID: id()}
	data := "private photo fixture"
	digest := sha256.Sum256([]byte(data))
	upload, err := service.Prepare(ctx, p, artifact.PrepareRequest{WriteID: id(),
		Source: artifact.Source{Kind: "user_import", ProjectID: id()}, MIME: "image/png",
		Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Put(ctx, p, upload.UploadID, strings.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	version, err := service.Commit(ctx, p, upload.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Prepare(ctx, p, artifact.PrepareRequest{WriteID: id(), ArtifactID: version.ArtifactID,
		Source: upload.Source, MIME: "image/png", Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Put(ctx, p, second.UploadID, strings.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	otherVersion, err := service.Commit(ctx, p, second.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{service: service}
	args, _ := json.Marshal(map[string]any{"content_ref": version.ContentRef, "reason": "test cleanup"})
	other := p
	other.OrganizationID = id()
	if _, err := s.deleteMCP(ctx, other, args); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal("cross-organization delete", err)
	}
	result, err := s.deleteMCP(ctx, p, args)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["status"] != "deleted" {
		t.Fatal("missing deletion receipt")
	}
	if _, err := service.Get(ctx, p, version.ContentRef, artifact.Access{}); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal("deleted metadata readable", err)
	}
	if reader, err := blobs.Open(ctx, storage.Object{ID: version.ObjectID, SHA256: version.SHA256, Size: version.Size}); !os.IsNotExist(err) {
		if reader != nil {
			reader.Close()
		}
		t.Fatal("deleted physical content remains", err)
	}
	if _, err := s.deleteMCP(ctx, p, args); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal("repeat deletion", err)
	}
	if _, err := service.Get(ctx, p, otherVersion.ContentRef, artifact.Access{}); err != nil {
		t.Fatal("other version removed", err)
	}
	if err := blobs.Verify(ctx, storage.Object{ID: otherVersion.ObjectID, SHA256: otherVersion.SHA256, Size: otherVersion.Size}); err != nil {
		t.Fatal("other version physical content removed", err)
	}
}

func TestMCPIdentityAndNotifications(t *testing.T) {
	token := strings.Repeat("s", 32)
	s, err := New(&artifact.Service{}, token)
	if err != nil {
		t.Fatal(err)
	}
	request := func(body string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+token)
			for _, h := range []string{"X-User-Id", "X-Organization-Id", "X-Request-Id"} {
				r.Header.Set(h, id())
			}
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if r := request(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, false); r.Code != 403 {
		t.Fatal(r.Code)
	}
	if r := request(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, true); r.Code != 200 || !strings.Contains(r.Body.String(), "artifact.write") {
		t.Fatal(r.Code)
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"artifact.write","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"artifact.write","arguments":{"mode":"text","mode":"prepare"}}}`,
	} {
		if r := request(body, true); r.Code != 400 {
			t.Fatal("invalid envelope accepted", r.Code)
		}
	}
}
