package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type stalledUpload struct {
	entered chan struct{}
	release chan struct{}
}

func (r *stalledUpload) Read([]byte) (int, error) {
	close(r.entered)
	<-r.release
	return 0, io.ErrUnexpectedEOF
}

func TestS3StalledUploadDoesNotBlockOtherObject(t *testing.T) {
	body := []byte("already stored video")
	hash := sha256.Sum256(body)
	o := Object{ID: uuid.Must(uuid.NewV7()).String(), SHA256: hex.EncodeToString(hash[:]), Size: int64(len(body))}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("unexpected method %s", r.Method)
		}
		w.Header().Set("Content-Length", stringInt(len(body)))
		w.Header().Set("X-Amz-Meta-Sha256", o.SHA256)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	}))
	defer server.Close()
	s, err := NewS3(strings.TrimPrefix(server.URL, "http://"), "artifact", "access", "secret", "us-east-1", false, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	stalled := &stalledUpload{make(chan struct{}), make(chan struct{})}
	done := make(chan error, 1)
	other := o
	other.ID = uuid.Must(uuid.NewV7()).String()
	go func() { _, err := s.Put(t.Context(), other, stalled); done <- err }()
	<-stalled.entered
	defer func() { close(stalled.release); <-done }()
	result := make(chan error, 1)
	go func() {
		reused, err := s.Put(t.Context(), o, bytes.NewReader(body))
		if err == nil && !reused {
			err = errors.New("existing object was not reused")
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unrelated upload blocked by stalled request body")
	}
}

func TestS3SameObjectWaitIsCancellable(t *testing.T) {
	s := &S3{}
	unlock, err := s.lockObject(t.Context(), "same")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.lockObject(ctx, "same"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	other, err := s.lockObject(t.Context(), "other")
	if err != nil {
		t.Fatal(err)
	}
	other()
	unlock()
	if len(s.puts) != 0 {
		t.Fatal("object locks leaked")
	}
}
