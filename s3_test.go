package main

import (
	"errors"

	"github.com/minio/minio-go/v7"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// fakeS3Handler serves /{bucket}/{key} for both HEAD (stat) and GET:
// "ok/<n>bytes" -> n zero bytes; "404" -> NoSuchKey; "no-bucket" ->
// stat OK then NoSuchBucket on GET (Go's client drops HEAD bodies, so a
// missing bucket is indistinguishable from a missing object at stat time —
// the bucket "vanishes" between stat and get); "err" -> connection close.
func fakeS3Handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/multica-artifacts/")
		key = strings.TrimPrefix(key, "/")
		writeErr := func(code, msg string) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>` + code + `</Code><Message>` + msg + `</Message></Error>`))
		}
		switch {
		case key == "404":
			writeErr("NoSuchKey", "gone")
		case key == "no-bucket":
			if r.Method == http.MethodHead {
				// Stat succeeds: object "exists" (size 0)...
				w.Header().Set("Content-Length", "0")
				w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
				w.WriteHeader(http.StatusOK)
			} else {
				// ...then the bucket is gone by the time GET runs.
				writeErr("NoSuchBucket", "no such bucket")
			}
		case key == "err":
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("server cannot hijack")
			}
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		case strings.HasPrefix(key, "ok/"):
			n, err := strconv.Atoi(key[len("ok/"):])
			if err != nil {
				t.Fatalf("unexpected key %q", key)
			}
			w.Header().Set("Content-Length", strconv.Itoa(n))
			w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, string(make([]byte, n)))
			}
		default:
			writeErr("NoSuchKey", "gone")
		}
	})
	return mux
}

func newTestObjectStore(t *testing.T, s3Cap int64) *MinioObjectStore {
	t.Helper()
	ts := httptest.NewServer(fakeS3Handler(t))
	t.Cleanup(ts.Close)
	cfg := &S3Config{
		Bucket:       "multica-artifacts",
		Region:       "us-east-1",
		AccessKeyID:  "AK",
		SecretKey:    "SK",
		Endpoint:     ts.Listener.Addr().String(),
		Secure:       false,
		MaxFileBytes: s3Cap,
	}
	store, err := NewMinioObjectStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// newTestObjectStoreURL is newTestObjectStore for the standard
// AWS_ENDPOINT_URL form: cfg carries only the full URL (host extracted,
// scheme http -> Secure false); the legacy pair stays empty to pin that
// EndpointURL alone is sufficient.
func newTestObjectStoreURL(t *testing.T, s3Cap int64) *MinioObjectStore {
	t.Helper()
	ts := httptest.NewServer(fakeS3Handler(t))
	t.Cleanup(ts.Close)
	cfg := &S3Config{
		Bucket:       "multica-artifacts",
		Region:       "us-east-1",
		AccessKeyID:  "AK",
		SecretKey:    "SK",
		EndpointURL:  ts.URL,
		MaxFileBytes: s3Cap,
	}
	store, err := NewMinioObjectStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestNewMinioObjectStore_BadEndpointURL pins that malformed
// AWS_ENDPOINT_URL values fail construction (startup fatal) rather than
// producing a silently misconfigured client.
func TestNewMinioObjectStore_BadEndpointURL(t *testing.T) {
	for name, raw := range map[string]string{
		"no scheme or host": "nohost",
		"missing protocol":  "://bad",
	} {
		_, err := NewMinioObjectStore(&S3Config{
			Bucket:       "multica-artifacts",
			Region:       "us-east-1",
			AccessKeyID:  "AK",
			SecretKey:    "SK",
			EndpointURL:  raw,
			MaxFileBytes: 1 << 20,
		})
		if err == nil {
			t.Errorf("%s: NewMinioObjectStore(%q) err = nil, want error", name, raw)
			continue
		}
		if msg := err.Error(); msg == "" {
			t.Errorf("%s: empty error message", name)
		}
	}
}

func TestObjectStore_Get_OK(t *testing.T) {
	store := newTestObjectStore(t, 1<<20)
	data, err := store.Get("ok/5", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 5 {
		t.Fatalf("len = %d, want 5", len(data))
	}
}

// TestObjectStore_Get_EndpointURLOK mirrors TestObjectStore_Get_OK through
// the standard AWS_ENDPOINT_URL config form (scheme-derived Secure, host
// extracted from the URL) instead of the legacy Endpoint/Secure pair.
func TestObjectStore_Get_EndpointURLOK(t *testing.T) {
	store := newTestObjectStoreURL(t, 1<<20)
	data, err := store.Get("ok/5", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 5 {
		t.Fatalf("len = %d, want 5", len(data))
	}
}

func TestObjectStore_Get_ObjectNotFound(t *testing.T) {
	store := newTestObjectStore(t, 1<<20)
	_, err := store.Get("404", 1024)
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want ErrObjectNotFound", err)
	}
}

func TestObjectStore_Get_S3CapTooLarge(t *testing.T) {
	// Object 10 bytes, S3 cap 5: S3-layer rejection, distinct sentinel.
	store := newTestObjectStore(t, 5)
	_, err := store.Get("ok/10", 1024)
	if !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("err = %v, want ErrObjectTooLarge", err)
	}
}

func TestObjectStore_Get_AttachmentCapTooLarge(t *testing.T) {
	// Object 10 bytes, S3 cap 1MiB, attachment limit 5: attachment layer.
	store := newTestObjectStore(t, 1<<20)
	_, err := store.Get("ok/10", 5)
	if !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("err = %v, want ErrAttachmentTooLarge", err)
	}
}

func TestObjectStore_Get_AtBoundaryOK(t *testing.T) {
	store := newTestObjectStore(t, 5)
	if _, err := store.Get("ok/5", 5); err != nil {
		t.Fatalf("at-limit Get err = %v, want nil", err)
	}
}

func TestObjectStore_Get_OtherErrorSanitized(t *testing.T) {
	store := newTestObjectStore(t, 1<<20)
	_, err := store.Get("err", 1024)
	if err == nil || errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want a non-sentinel error", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "multica-artifacts") || strings.Contains(msg, "127.0.0.1") {
		t.Errorf("error leaks internals: %s", msg)
	}
}

func TestObjectStore_Get_NoSuchBucket(t *testing.T) {
	store := newTestObjectStore(t, 1<<20)
	_, err := store.Get("no-bucket", 1024)
	if err == nil || errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want a non-ErrObjectNotFound error", err)
	}
	if !strings.Contains(err.Error(), "bucket") {
		t.Errorf("err should hint at bucket config: %v", err)
	}
}

// TestBucketLookupTriState pins the design-doc contract: explicit
// S3_USE_PATH_STYLE=0/false/no means BucketLookupDNS even when path-style
// would be inferred; explicit true means BucketLookupPath; unset means Auto
// (minio-go infers: custom endpoint -> path-style, native -> virtual-host).
func TestBucketLookupTriState(t *testing.T) {
	tests := []struct {
		name              string
		usePathStyle      bool
		pathStyleExplicit bool
		want              minio.BucketLookupType
	}{
		{"explicit false forces DNS", false, true, minio.BucketLookupDNS},
		{"explicit true forces path", true, true, minio.BucketLookupPath},
		{"unset leaves auto", true, false, minio.BucketLookupAuto},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bucketLookup(&S3Config{UsePathStyle: tt.usePathStyle, PathStyleExplicit: tt.pathStyleExplicit}); got != tt.want {
				t.Fatalf("bucketLookup = %v, want %v", got, tt.want)
			}
		})
	}
}
