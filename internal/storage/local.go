// Package storage stores immutable payloads. Business visibility, authorization,
// provenance and retention are owned by the Artifact service and its database.
package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
)

var (
	ErrInvalid  = errors.New("invalid content declaration")
	ErrMismatch = errors.New("content size or digest mismatch")
	ErrConflict = errors.New("immutable content already exists with different bytes")
	ErrCorrupt  = errors.New("stored content failed integrity verification")
	idPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Object ID is allocated and durably bound to a write operation before Put.
// It is not a client-supplied file path or a globally deduplicated content hash.
type Object struct {
	ID     string
	SHA256 string
	Size   int64
}

// Reader is a content stream that validates the declared size and SHA-256 as
// it reaches EOF. Call Verify when a caller must establish readiness before
// publishing metadata; callers must consume the stream or close it when
// abandoning a read.
type Reader interface {
	io.Reader
	io.Closer
}

// Backend stores immutable content objects. Object IDs are service-generated
// identities, never client paths or content-hash deduplication keys.
type Backend interface {
	Put(context.Context, Object, io.Reader) (reused bool, err error)
	Open(context.Context, Object) (Reader, error)
	Verify(context.Context, Object) error
	Close() error
}

type Local struct {
	root     *os.Root
	maxBytes int64
}

func NewLocal(directory string, maxBytes int64) (*Local, error) {
	if maxBytes <= 0 || maxBytes > 1<<50 {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create content root: %w", err)
	}
	r, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"objects", "staging"} {
		if err := r.MkdirAll(name, 0700); err != nil {
			r.Close()
			return nil, err
		}
	}
	return &Local{root: r, maxBytes: maxBytes}, nil
}

func (l *Local) Close() error { return l.root.Close() }

func (l *Local) valid(o Object) bool {
	return idPattern.MatchString(o.ID) && hashPattern.MatchString(o.SHA256) && o.Size >= 0 && o.Size <= l.maxBytes
}

// Put verifies the entire input, flushes it, then links it into the immutable
// namespace without replacement. An interrupted metadata commit can retry with
// the same object ID; a partial staging file is never a published payload.
// The caller must commit metadata/retention before exposing this object.
func (l *Local) Put(ctx context.Context, o Object, input io.Reader) (reused bool, err error) {
	if !l.valid(o) || input == nil {
		return false, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	temp := "staging/" + rand.Text()
	f, err := l.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	defer func() { f.Close(); l.root.Remove(temp) }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(contextReader{ctx, input}, o.Size+1))
	if err != nil {
		return false, err
	}
	if n != o.Size || hex.EncodeToString(h.Sum(nil)) != o.SHA256 {
		return false, ErrMismatch
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	target := "objects/" + o.ID
	if err := l.root.Link(temp, target); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return false, err
		}
		existing, err := l.Open(ctx, o)
		if errors.Is(err, ErrCorrupt) {
			return false, ErrConflict
		}
		if err != nil {
			return false, err
		}
		if err := existing.Close(); err != nil {
			return false, err
		}
		// A prior attempt may have linked successfully but failed directory sync.
		if err := syncObjects(l.root); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := syncObjects(l.root); err != nil {
		return false, err
	}
	return false, nil
}

// Open returns only a verified regular file positioned at the beginning. It
// never trusts a caller's URL, absolute path or on-disk staging object.
func (l *Local) Open(ctx context.Context, o Object) (Reader, error) {
	if !l.valid(o) {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := "objects/" + o.ID
	info, err := l.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrCorrupt
	}
	f, err := l.root.Open(name)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != o.Size {
		return nil, ErrCorrupt
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(contextReader{ctx, f}, o.Size+1))
	if err != nil {
		return nil, err
	}
	if n != o.Size || hex.EncodeToString(h.Sum(nil)) != o.SHA256 {
		return nil, ErrCorrupt
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	ok = true
	return f, nil
}

func (l *Local) Verify(ctx context.Context, o Object) error {
	f, err := l.Open(ctx, o)
	if err != nil {
		return err
	}
	return f.Close()
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
