package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
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
