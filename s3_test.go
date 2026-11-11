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
// "err" → connection close (transport error, not an S3 error response).
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
	// ErrObjectTooLarge is returned bare (no key context to add).
	if !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("err = %v, want ErrObjectTooLarge", err)
	}
}

func TestObjectStore_Get_OtherError(t *testing.T) {
	store := newTestObjectStore(t)
	_, err := store.Get("err", 1024)
	if err == nil || err == ErrObjectNotFound || err == ErrObjectTooLarge {
		t.Fatalf("err = %v, want a wrapped transport error", err)
	}
}
