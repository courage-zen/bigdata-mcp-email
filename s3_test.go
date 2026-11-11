package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// newFakeS3 serves GET /{bucket}/{key} with canned behavior per key.
// Keys: "ok/<n>bytes" → n zero bytes; "404" → S3 NoSuchKey XML error;
// "no-bucket" → 404 with NoSuchBucket (must NOT map to ErrObjectNotFound);
// "err" → connection close (transport error, not an S3 error response);
// anything else → 404 NoSuchKey (safe default, no handler panic).
func newFakeS3(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Path-style addressing: the bucket is the first path segment
		// (minio-go uses path style for non-Amazon endpoints), so the
		// key is everything after it.
		key := strings.TrimPrefix(r.URL.Path, "/multica-artifacts/")
		key = strings.TrimPrefix(key, "/")
		if key == "404" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>gone</Message></Error>`))
			return
		}
		if key == "no-bucket" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchBucket</Code><Message>no such bucket</Message></Error>`))
			return
		}
		if key == "err" {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("server cannot hijack")
			}
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		// "ok/<n>"
		if !strings.HasPrefix(key, "ok/") {
			// Unexpected keys (too short to slice safely) 404 instead of
			// panicking the handler goroutine.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>gone</Message></Error>`))
			return
		}
		n, err := strconv.Atoi(key[len("ok/"):])
		if err != nil {
			t.Fatalf("unexpected key %q", key)
		}
		// minio-go parses the object info from headers and requires a valid
		// RFC 7231 Last-Modified (and Content-Length) on 200 responses.
		w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, string(make([]byte, n)))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, ts.Listener.Addr().String()
}

func newTestObjectStore(t *testing.T) *MinioObjectStore {
	t.Helper()
	_, addr := newFakeS3(t)
	// minio.New prepends the scheme itself from the secure flag, and rejects
	// scheme-prefixed endpoints ("cannot have fully qualified paths") — so
	// pass the bare host:port (probed against minio-go v7.2.1).
	store, err := NewMinioObjectStore(addr, "AK", "SK", "multica-artifacts", false)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestObjectStore_Get_OK(t *testing.T) {
	store := newTestObjectStore(t)
	data, err := store.Get("ok/5", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 5 {
		t.Fatalf("len = %d, want 5", len(data))
	}
}

func TestObjectStore_Get_ObjectNotFound(t *testing.T) {
	store := newTestObjectStore(t)
	_, err := store.Get("404", 1024)
	// mapS3Error wraps the sentinel with the key, so match via errors.Is.
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want ErrObjectNotFound", err)
	}
}

func TestObjectStore_Get_TooLarge(t *testing.T) {
	store := newTestObjectStore(t)
	_, err := store.Get("ok/10", 5)
	if !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("err = %v, want ErrObjectTooLarge", err)
	}
	// Boundary: exactly at the limit is OK — the limit+1 read distinguishes
	// "at limit" from "more to come", so this pins that behavior.
	if _, err := store.Get("ok/5", 5); err != nil {
		t.Fatalf("at-limit Get err = %v, want nil", err)
	}
}

func TestObjectStore_Get_OtherError(t *testing.T) {
	store := newTestObjectStore(t)
	_, err := store.Get("err", 1024)
	if err == nil || errors.Is(err, ErrObjectNotFound) || errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("err = %v, want a wrapped transport error", err)
	}
}

// TestObjectStore_Get_NoSuchBucket pins that a missing bucket (or any other
// non-object 404) is a generic error, not ErrObjectNotFound — the operator
// should hear "check bucket/endpoint", not "check your key".
func TestObjectStore_Get_NoSuchBucket(t *testing.T) {
	store := newTestObjectStore(t)
	_, err := store.Get("no-bucket", 1024)
	if err == nil {
		t.Fatal("err = nil, want an error")
	}
	if errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want a non-ErrObjectNotFound error (NoSuchBucket)", err)
	}
}
