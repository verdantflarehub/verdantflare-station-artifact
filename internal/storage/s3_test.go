package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

var _ Backend = (*Local)(nil)
var _ Backend = (*S3)(nil)

func TestNewS3RequiresExplicitCredentialsAndEndpoint(t *testing.T) {
	for name, endpoint := range map[string]string{
		"missing endpoint":     "",
		"endpoint with scheme": "https://s3.example.test",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewS3(endpoint, "artifact", "access", "secret", "", true, 1<<20); err != ErrInvalid {
				t.Fatalf("NewS3(%q) error = %v, want ErrInvalid", endpoint, err)
			}
		})
	}
	if _, err := NewS3("s3.example.test", "artifact", "", "secret", "", true, 1<<20); err != ErrInvalid {
		t.Fatalf("missing access key error = %v, want ErrInvalid", err)
	}
	if _, err := NewS3("s3.example.test", "artifact", "access", "secret", "", true, 1<<20); err != nil {
		t.Fatalf("valid S3 configuration rejected: %v", err)
	}
}

func TestS3PutOpenAndIntegrity(t *testing.T) {
	type stored struct {
		body []byte
		hash string
	}
	var mu sync.Mutex
	objects := map[string]stored{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/artifact/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodHead:
			obj, ok := objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", stringInt(len(obj.body)))
			w.Header().Set("X-Amz-Meta-Sha256", obj.hash)
			w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
			w.Header().Set("ETag", `"test"`)
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			objects[key] = stored{body: body, hash: r.Header.Get("X-Amz-Meta-Sha256")}
		case http.MethodGet:
			obj, ok := objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", stringInt(len(obj.body)))
			w.Header().Set("X-Amz-Meta-Sha256", obj.hash)
			w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
			w.Header().Set("ETag", `"test"`)
			_, _ = w.Write(obj.body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	backend, err := NewS3(parsed.Host, "artifact", "access", "secret", "us-east-1", false, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("project manifest\n")
	digest := sha256.Sum256(body)
	o := Object{ID: uuid.Must(uuid.NewV7()).String(), SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}
	reused, err := backend.Put(t.Context(), o, bytes.NewReader(body))
	if err != nil || reused {
		mu.Lock()
		storedObject := objects["objects/"+o.ID]
		mu.Unlock()
		t.Fatalf("first Put = reused %v, err %v, stored size=%d hash=%q want=%q", reused, err, len(storedObject.body), storedObject.hash, o.SHA256)
	}
	reused, err = backend.Put(t.Context(), o, bytes.NewReader(body))
	if err != nil || !reused {
		t.Fatalf("retry Put = reused %v, err %v", reused, err)
	}
	reader, err := backend.Open(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("Open = %q, err %v", got, err)
	}
	mu.Lock()
	objects["objects/"+o.ID] = stored{body: bytes.Repeat([]byte{'x'}, len(body)), hash: o.SHA256}
	mu.Unlock()
	reader, err = backend.Open(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(reader)
	reader.Close()
	if err != ErrCorrupt {
		t.Fatalf("corrupt Open error = %v, want ErrCorrupt", err)
	}
}

func stringInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
