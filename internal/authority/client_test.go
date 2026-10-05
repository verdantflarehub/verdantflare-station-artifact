package authority

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
)

func TestLiveDecisionFailsClosed(t *testing.T) {
	const id = "019a00b0-0000-7000-8000-000000000001"
	p := artifact.Principal{OrganizationID: id, SubjectID: id, RequestID: id}
	var body atomic.Value
	body.Store(`{"allowed":true}`)
	var status atomic.Int32
	status.Store(200)
	var calls atomic.Int32
	token := strings.Repeat("x", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Request-Id") != id {
			t.Error("missing trusted transport fields")
		}
		w.Header().Set("Location", "/redirect")
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer server.Close()
	c, err := New(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Authorize(context.Background(), p, artifact.Permission{Action: "read"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status int
		body   string
		err    error
	}{
		{200, `{"allowed":false}`, artifact.ErrForbidden},
		{200, `{"Allowed":true}`, artifact.ErrDependency},
		{200, `{"allowed":true,"allowed":false}`, artifact.ErrDependency},
		{200, `{}`, artifact.ErrDependency},
		{200, `{"allowed":null}`, artifact.ErrDependency},
		{200, `{"allowed":true,"extra":true}`, artifact.ErrDependency},
		{200, `{"allowed":true}` + strings.Repeat(" ", 1024), artifact.ErrDependency},
		{403, `{"allowed":true}`, artifact.ErrForbidden},
		{503, `{"allowed":true}`, artifact.ErrDependency},
		{307, `{"allowed":true}`, artifact.ErrDependency},
	} {
		status.Store(int32(tc.status))
		body.Store(tc.body)
		before := calls.Load()
		if err := c.Authorize(context.Background(), p, artifact.Permission{Action: "read"}); !errors.Is(err, tc.err) {
			t.Fatalf("status %d body %s: %v", tc.status, tc.body, err)
		}
		if calls.Load() != before+1 {
			t.Fatal("decision cached or redirect followed")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(c.Authorize(ctx, p, artifact.Permission{Action: "read"}), artifact.ErrDependency) {
		t.Fatal("canceled authority request allowed")
	}
	server.Close()
	if !errors.Is(c.Authorize(context.Background(), p, artifact.Permission{Action: "read"}), artifact.ErrDependency) {
		t.Fatal("unavailable authority allowed")
	}
}
func TestRejectInsecureConfiguration(t *testing.T) {
	for _, endpoint := range []string{"http://example.com/check", "https://user:password@example.com/check", "https://example.com/check?token=x", "file:///tmp/x", "https://example.com/check#fragment"} {
		if _, err := New(endpoint, strings.Repeat("x", 32)); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
}
