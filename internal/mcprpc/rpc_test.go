package mcprpc

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEnvelopeAndOpaqueArguments(t *testing.T) {
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":null,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"project.list","arguments":{"x":1,"x":2}}}`,
		`{"jsonrpc":"2.0","ID":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":null}`,
		`[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`,
		`{"jsonrpc":"2.0","id":9007199254740992,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"} {}`,
	} {
		if _, e := Decode(strings.NewReader(body)); e == nil {
			t.Fatalf("invalid envelope accepted: %s", body)
		}
	}
	r, e := Decode(strings.NewReader(`{"jsonrpc":"2.0","id":"commit","method":"tools/call","params":{"name":"project.commit","arguments":{"manifest":{"extensions":{"domain.value":null}}}}}`))
	if e != nil {
		t.Fatal(e)
	}
	name, args, _, e := r.Call()
	if e != nil || name != "project.commit" || args["manifest"] == nil {
		t.Fatal("opaque domain JSON rejected", e)
	}
}

func TestRejectedCloseConnectionDeliversResponse(t *testing.T) {
	entered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		Reject(w, r, Request{}, 403, -32000, "Forbidden")
	}))
	defer srv.Close()
	for i := 0; i < 20; i++ {
		conn, err := net.DialTimeout("tcp", strings.TrimPrefix(srv.URL, "http://"), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, err = io.WriteString(conn, "POST /mcp HTTP/1.1\r\nHost: localhost\r\nContent-Length: 2\r\nConnection: close\r\n\r\n")
		if err != nil {
			conn.Close()
			t.Fatal(err)
		}
		select {
		case <-entered:
		case <-time.After(time.Second):
			conn.Close()
			t.Fatal("handler did not receive headers")
		}
		// Delay the body as a client with separate header/body writes can do.
		time.Sleep(5 * time.Millisecond)
		_, _ = io.WriteString(conn, "{}")
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			conn.Close()
			t.Fatal("rejection lost to connection reset", err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		conn.Close()
		if readErr != nil || response.StatusCode != 403 || !strings.Contains(string(body), "Forbidden") {
			t.Fatal("incomplete rejection", response.StatusCode, readErr)
		}
	}
}
