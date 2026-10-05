package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

const objectID = "0199b501-0000-7000-8000-000000000001"

func object(data []byte) Object {
	h := sha256.Sum256(data)
	return Object{objectID, hex.EncodeToString(h[:]), int64(len(data))}
}
func store(t *testing.T) (*Local, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewLocal(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func TestImmutableContentSurvivesRestart(t *testing.T) {
	s, dir := store(t)
	data := []byte("# 小月\n选定正面参考。\n")
	o := object(data)
	if reused, err := s.Put(context.Background(), o, bytes.NewReader(data)); err != nil || reused {
		t.Fatal(reused, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := NewLocal(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f, err := s.Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	actual, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("readback mismatch", err)
	}
	if reused, err := s.Put(context.Background(), o, bytes.NewReader(data)); err != nil || !reused {
		t.Fatal("retry did not reuse", reused, err)
	}
	other := []byte("replacement")
	if _, err := s.Put(context.Background(), object(other), bytes.NewReader(other)); !errors.Is(err, ErrConflict) {
		t.Fatal("replacement was not rejected", err)
	}
}

func TestInvalidOrPartialWritesStayInvisible(t *testing.T) {
	for name, reader := range map[string]io.Reader{"short": bytes.NewReader([]byte("ab")), "long": bytes.NewReader([]byte("abcd")), "wrong digest": bytes.NewReader([]byte("xyz")), "interrupted": &failedReader{}} {
		t.Run(name, func(t *testing.T) {
			s, dir := store(t)
			o := object([]byte("abc"))
			if _, err := s.Put(context.Background(), o, reader); err == nil {
				t.Fatal("invalid write succeeded")
			}
			if _, err := s.Open(context.Background(), o); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("partial write became visible", err)
			}
			files, err := os.ReadDir(filepath.Join(dir, "staging"))
			if err != nil || len(files) != 0 {
				t.Fatal("failed staging not cleaned", err)
			}
		})
	}
}

type failedReader struct{}

func (*failedReader) Read(p []byte) (int, error) { copy(p, "a"); return 1, io.ErrUnexpectedEOF }

func TestConcurrentRetriesNeverReplace(t *testing.T) {
	s, _ := store(t)
	data := []byte("same content")
	o := object(data)
	var wg sync.WaitGroup
	var first atomic.Int32
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reused, err := s.Put(context.Background(), o, bytes.NewReader(data))
			if err != nil {
				t.Error(err)
			}
			if !reused && err == nil {
				first.Add(1)
			}
		}()
	}
	wg.Wait()
	if first.Load() != 1 {
		t.Fatalf("expected one publication, got %d", first.Load())
	}
}

func TestCancellationAndTraversal(t *testing.T) {
	s, _ := store(t)
	data := []byte("abc")
	o := object(data)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, o, bytes.NewReader(data)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, key := range []string{"../outside", "C:/outside", "/outside", ".", "objects/file"} {
		bad := o
		bad.ID = key
		if _, err := s.Put(context.Background(), bad, bytes.NewReader(data)); !errors.Is(err, ErrInvalid) {
			t.Fatal("unsafe identity accepted", key)
		}
	}
	o.Size = 1 << 21
	if _, err := s.Put(context.Background(), o, bytes.NewReader(data)); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized declaration accepted")
	}
}

func TestCorruptionDetectedBeforeRead(t *testing.T) {
	s, dir := store(t)
	data := []byte("abc")
	o := object(data)
	if _, err := s.Put(context.Background(), o, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "objects", o.ID), []byte("xyz"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(context.Background(), o); !errors.Is(err, ErrCorrupt) {
		t.Fatal("corrupted content returned", err)
	}
}

func TestBinaryAndEmptyObjects(t *testing.T) {
	for _, data := range [][]byte{{}, {0, 255, 1, 2, 128}} {
		s, _ := store(t)
		o := object(data)
		if _, err := s.Put(context.Background(), o, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		f, err := s.Open(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(f)
		f.Close()
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("binary roundtrip failed", err)
		}
	}
}
