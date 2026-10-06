package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 is an S3-compatible immutable object backend. The service-generated
// object ID is the only physical key component. PostgreSQL remains the source
// of version, retention and authorization truth.
type S3 struct {
	mu       sync.Mutex
	client   *minio.Client
	bucket   string
	prefix   string
	maxBytes int64
}

func NewS3(endpoint, bucket, accessKey, secretKey, region string, secure bool, maxBytes int64) (*S3, error) {
	endpoint = strings.TrimSpace(endpoint)
	bucket = strings.TrimSpace(bucket)
	if endpoint == "" || strings.Contains(endpoint, "/") || bucket == "" ||
		strings.ContainsAny(bucket, " /\\") || accessKey == "" || secretKey == "" ||
		maxBytes <= 0 || maxBytes > 1<<50 {
		return nil, ErrInvalid
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}
	return &S3{client: client, bucket: bucket, prefix: "objects/", maxBytes: maxBytes}, nil
}

func (s *S3) Close() error { return nil }

// Ready verifies the configured bucket without creating it. Bucket creation
// belongs to the deployment owner so an Artifact credential cannot silently
// create or select a different bucket. Failing closed here makes a missing
// bucket visible before the first project commit.
func (s *S3) Ready(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check S3 bucket: %w", err)
	}
	if !exists {
		return ErrBucketNotFound
	}
	return nil
}

func (s *S3) valid(o Object) bool {
	return idPattern.MatchString(o.ID) && hashPattern.MatchString(o.SHA256) && o.Size >= 0 && o.Size <= s.maxBytes
}

func (s *S3) key(o Object) string { return path.Join(s.prefix, o.ID) }

func s3NotFound(err error) bool {
	r := minio.ToErrorResponse(err)
	return r.Code == "NoSuchKey" || r.Code == "NotFound"
}

// Put stages and verifies bytes before uploading. A per-process object key is
// never overwritten: an existing object is reused only when its declared
// size and digest match. Artifact holds a database advisory lock around this
// operation so replicas sharing its database serialize the existence check.
func (s *S3) Put(ctx context.Context, o Object, input io.Reader) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.valid(o) || input == nil {
		return false, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp("", "station-artifact-s3-")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(contextReader{ctx, input}, o.Size+1))
	if copyErr != nil {
		tmp.Close()
		return false, copyErr
	}
	if n != o.Size || hex.EncodeToString(h.Sum(nil)) != o.SHA256 {
		tmp.Close()
		return false, ErrMismatch
	}
	if err = ctx.Err(); err != nil {
		tmp.Close()
		return false, err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if _, err = tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		return false, err
	}
	stat, err := s.client.StatObject(ctx, s.bucket, s.key(o), minio.StatObjectOptions{})
	if err == nil {
		closeErr := tmp.Close()
		if closeErr != nil {
			return false, closeErr
		}
		if stat.Size != o.Size || strings.ToLower(stat.Metadata.Get("X-Amz-Meta-Sha256")) != o.SHA256 {
			return false, ErrConflict
		}
		return true, nil
	}
	if !s3NotFound(err) {
		tmp.Close()
		return false, err
	}
	_, err = s.client.PutObject(ctx, s.bucket, s.key(o), tmp, o.Size, minio.PutObjectOptions{
		ContentType:          "application/octet-stream",
		DisableContentSha256: true,
		UserMetadata:         map[string]string{"sha256": o.SHA256},
	})
	closeErr := tmp.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	stat, err = s.client.StatObject(ctx, s.bucket, s.key(o), minio.StatObjectOptions{})
	if err != nil {
		return false, err
	}
	if stat.Size != o.Size || strings.ToLower(stat.Metadata.Get("X-Amz-Meta-Sha256")) != o.SHA256 {
		return false, ErrConflict
	}
	return false, nil
}

func (s *S3) Open(ctx context.Context, o Object) (Reader, error) {
	if !s.valid(o) {
		return nil, ErrInvalid
	}
	obj, err := s.client.GetObject(ctx, s.bucket, s.key(o), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	stat, err := obj.Stat()
	if err != nil {
		obj.Close()
		if s3NotFound(err) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	if stat.Size != o.Size || (stat.Metadata.Get("X-Amz-Meta-Sha256") != "" && strings.ToLower(stat.Metadata.Get("X-Amz-Meta-Sha256")) != o.SHA256) {
		obj.Close()
		return nil, ErrCorrupt
	}
	return &verifiedObject{ctx: ctx, object: obj, expectedSize: o.Size, expectedSHA: o.SHA256}, nil
}

func (s *S3) Verify(ctx context.Context, o Object) error {
	reader, err := s.Open(ctx, o)
	if err != nil {
		return err
	}
	_, readErr := io.Copy(io.Discard, reader)
	closeErr := reader.Close()
	if readErr != nil {
		return readErr
	}
	return closeErr
}

type verifiedObject struct {
	ctx          context.Context
	object       *minio.Object
	expectedSize int64
	expectedSHA  string
	h            hash.Hash
	n            int64
	verified     bool
}

func (v *verifiedObject) Read(p []byte) (int, error) {
	if err := v.ctx.Err(); err != nil {
		return 0, err
	}
	if v.h == nil {
		v.h = sha256.New()
	}
	n, err := v.object.Read(p)
	if n > 0 {
		v.n += int64(n)
		_, _ = v.h.Write(p[:n])
	}
	if errors.Is(err, io.EOF) {
		if v.n != v.expectedSize || hex.EncodeToString(v.h.Sum(nil)) != v.expectedSHA {
			return n, ErrCorrupt
		}
		v.verified = true
	}
	return n, err
}

func (v *verifiedObject) Close() error { return v.object.Close() }
