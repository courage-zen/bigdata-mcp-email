package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ObjectStore reads attachment objects from the configured bucket. The
// interface keeps the tool testable, mirroring MailSender.
type ObjectStore interface {
	// Get fetches key, reading at most limit bytes: larger objects return
	// ErrObjectTooLarge without buffering. A missing object returns
	// ErrObjectNotFound; anything else wraps the underlying error.
	Get(key string, limit int64) ([]byte, error)
}

var (
	ErrObjectNotFound = errors.New("s3 object not found")
	ErrObjectTooLarge = errors.New("s3 object exceeds attachment size limit")
)

// MinioObjectStore is the ObjectStore over a minio-go client.
type MinioObjectStore struct {
	client *minio.Client
	bucket string
}

// NewMinioObjectStore builds a store. endpoint is bare host:port — minio.New
// derives the scheme from the secure flag (http when false, the in-cluster
// plaintext trust model) and rejects scheme-prefixed endpoints.
// Region is set so minio-go signs immediately instead of issuing a
// GET /{bucket}?location= probe on first use (v7.2.1 does that whenever
// Region is empty); us-east-1 is MinIO's default region. Assumption: the
// hardcoded region also disables minio-go's region self-healing (the
// bucket-location probe that recovers a wrong region only runs when region
// is empty), so a deployment with a non-default MINIO_REGION would fail
// every Get with a signature error — an s3.region config option is the
// future escape hatch.
// A malformed endpoint is a startup failure: config was validated already,
// so the caller (main.go) treats the error as fatal.
func NewMinioObjectStore(endpoint, accessKey, secretKey, bucket string, secure bool) (*MinioObjectStore, error) {
	cli, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
		Region: "us-east-1",
	})
	if err != nil {
		return nil, fmt.Errorf("s3 endpoint %q: %w", endpoint, err)
	}
	return &MinioObjectStore{client: cli, bucket: bucket}, nil
}

// Get implements ObjectStore.
func (s *MinioObjectStore) Get(key string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, mapS3Error(key, err)
	}
	defer obj.Close()

	// limit+1 detects oversize in one read: reading exactly limit bytes can't
	// distinguish "exactly at limit" from "more to come".
	data, err := io.ReadAll(io.LimitReader(obj, limit+1))
	if err != nil {
		return nil, mapS3Error(key, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s", ErrObjectTooLarge, key)
	}
	return data, nil
}

// mapS3Error folds minio error responses onto the store's sentinel errors.
func mapS3Error(key string, err error) error {
	resp := minio.ToErrorResponse(err)
	// ToErrorResponse returns a zero value for transport errors (no XML
	// error body), so gate on the canonical missing-object codes. A bare
	// 404 is deliberately NOT treated as missing: minio-go synthesizes
	// Code "NoSuchKey" for 404s on an object (api-error-response.go), so
	// genuine missing objects still map — while NoSuchBucket or a proxy
	// 404 falls through to the wrapped error with the real code.
	if resp.Code == "NoSuchKey" || resp.Code == "NoSuchObject" {
		return fmt.Errorf("%w: %s", ErrObjectNotFound, key)
	}
	return fmt.Errorf("s3 get %q: %w", key, err)
}
