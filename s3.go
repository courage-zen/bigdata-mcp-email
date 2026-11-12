package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ObjectStore reads attachment objects from the configured bucket. The
// interface keeps the tool testable, mirroring MailSender.
type ObjectStore interface {
	// Get fetches key, reading at most limit bytes: larger objects fail
	// without buffering. A missing object returns ErrObjectNotFound; size
	// rejections distinguish the two caps (S3 transport vs attachment);
	// anything else wraps the underlying error — sanitized, no
	// bucket/key/endpoint in the message.
	Get(key string, limit int64) ([]byte, error)
}

var (
	ErrObjectNotFound     = errors.New("s3 object not found")
	ErrObjectTooLarge     = errors.New("s3 object exceeds the s3 transport size limit")
	ErrAttachmentTooLarge = errors.New("s3 object exceeds the attachment size limit")
)

// timeout constants (the standard's layered values): 10s connect, 60s
// response-header, 60s whole-download wall clock.
const (
	s3ConnectTimeout   = 10 * time.Second
	s3ReadTimeout      = 60 * time.Second
	s3DownloadDeadline = 60 * time.Second
)

// MinioObjectStore is the ObjectStore over a minio-go client.
type MinioObjectStore struct {
	client *minio.Client
	bucket string
	// maxBytes is the S3 transport cap (S3_MAX_FILE_BYTES).
	maxBytes int64
}

// NewMinioObjectStore builds a store from the resolved S3Config: the
// standard endpoint (full URL, scheme picks Secure) or the legacy bare
// host:port + Secure pair; EndpointURL, when set, takes precedence over
// the legacy pair. Layered timeouts are pinned on the transport
// (10s dial, 60s response header); Region is always set (skips minio-go's
// bucket-location probe); UsePathStyle forces the bucket addressing mode,
// defaulting to minio-go's auto inference (path-style for custom
// endpoints). A malformed endpoint is a startup failure.
func NewMinioObjectStore(cfg *S3Config) (*MinioObjectStore, error) {
	endpoint := cfg.Endpoint
	secure := cfg.Secure
	if cfg.EndpointURL != "" {
		u, err := url.Parse(cfg.EndpointURL)
		if err != nil {
			return nil, fmt.Errorf("s3: parse AWS_ENDPOINT_URL: %w", err)
		}
		if u.Host == "" {
			return nil, fmt.Errorf("s3: AWS_ENDPOINT_URL %q has no host", redactURL(cfg.EndpointURL))
		}
		endpoint = u.Host
		secure = u.Scheme == "https"
	}

	transport, err := minio.DefaultTransport(secure)
	if err != nil {
		return nil, fmt.Errorf("s3: default transport: %w", err)
	}
	transport.DialContext = (&net.Dialer{
		Timeout:   s3ConnectTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.ResponseHeaderTimeout = s3ReadTimeout

	lookup := minio.BucketLookupAuto
	if cfg.UsePathStyle {
		lookup = minio.BucketLookupPath
	}
	cli, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretKey, ""),
		Secure:       secure,
		Region:       cfg.Region,
		BucketLookup: lookup,
		Transport:    transport,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 endpoint %q: %w", endpoint, err)
	}
	return &MinioObjectStore{client: cli, bucket: cfg.Bucket, maxBytes: cfg.MaxFileBytes}, nil
}

// redactURL strips credentials and query from an endpoint URL for startup
// error messages (query may carry nothing today, but be safe by habit).
func redactURL(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		if u.User != nil {
			u.User = nil
		}
		u.RawQuery = ""
		return u.String()
	}
	return "<unparseable endpoint>"
}

// Get implements ObjectStore.
func (s *MinioObjectStore) Get(key string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s3DownloadDeadline)
	defer cancel()

	// Metadata precheck: refuse before streaming.
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return nil, mapError("stat", key, err)
	}
	if info.Size > s.maxBytes {
		return nil, ErrObjectTooLarge
	}
	if info.Size > limit {
		return nil, ErrAttachmentTooLarge
	}

	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, mapError("get", key, err)
	}
	defer obj.Close()

	// Stream-cap re-check (object replaced between Stat and Get): read
	// min(maxBytes, limit)+1, oversize maps like the precheck.
	maxRead := s.maxBytes
	if limit < maxRead {
		maxRead = limit
	}
	data, err := io.ReadAll(io.LimitReader(obj, maxRead+1))
	if err != nil {
		return nil, mapError("get", key, err)
	}
	if int64(len(data)) > maxRead {
		if limit < s.maxBytes {
			return nil, ErrAttachmentTooLarge
		}
		return nil, ErrObjectTooLarge
	}
	return data, nil
}

// mapError folds minio error responses onto the sentinel set. Messages
// surfaced to callers are sanitized: the minio error Code (e.g.
// "NoSuchKey", "AccessDenied") is safe to include; the raw error text is
// not (it embeds bucket/key/endpoint). The raw error goes to the log.
func mapError(stage, key string, err error) error {
	resp := minio.ToErrorResponse(err)
	if resp.Code == "NoSuchKey" || resp.Code == "NoSuchObject" {
		return fmt.Errorf("%w", ErrObjectNotFound)
	}
	log.Printf("s3 %s %q failed: %v", stage, key, err)
	switch resp.Code {
	case "NoSuchBucket":
		return fmt.Errorf("s3: bucket not found (check S3_BUCKET/endpoint)")
	case "AccessDenied":
		return fmt.Errorf("s3: access denied (check S3 credentials)")
	case "":
		return fmt.Errorf("s3: %s failed (transport error)", stage)
	default:
		return fmt.Errorf("s3: %s failed: code %s", stage, resp.Code)
	}
}
