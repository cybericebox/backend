package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// A missing object must read as ErrObjectNotFound: the reported bug returned a nil reader and a nil error,
// which the avatar route then streamed (panic, 500).
func TestGetReportsAMissingObjectInsteadOfANilReader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.Trim(r.URL.Path, "/")
		if !strings.Contains(path, "/") { // the bucket itself exists
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>no</Message></Error>`))
		}
	}))
	defer srv.Close()
	c, err := New(Config{Endpoint: strings.TrimPrefix(srv.URL, "http://"), AccessKey: "a", SecretKey: "b", Bucket: "bkt", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	rc, _, err := c.Get(context.Background(), "avatars/none")
	if rc != nil || !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("reader %v err %v", rc, err)
	}
}

// GetRange must ask the server for exactly bytes offset..offset+length-1 and return only those bytes.
func TestGetRange(t *testing.T) {
	object := []byte("0123456789abcdefghij") // 20 bytes
	var gotRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.Trim(r.URL.Path, "/")
		if !strings.Contains(path, "/") { // the bucket itself exists
			w.WriteHeader(http.StatusOK)
			return
		}
		gotRange = r.Header.Get("Range")
		var from, to int
		if _, err := fmt.Sscanf(gotRange, "bytes=%d-%d", &from, &to); err != nil {
			w.Header().Set("Content-Length", strconv.Itoa(len(object)))
			_, _ = w.Write(object)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, len(object)))
		w.Header().Set("Content-Length", strconv.Itoa(to-from+1))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Last-Modified", "Sat, 17 Oct 2026 10:00:00 GMT")
		w.WriteHeader(http.StatusPartialContent)
		if r.Method != http.MethodHead {
			_, _ = w.Write(object[from : to+1])
		}
	}))
	defer srv.Close()
	c, err := New(Config{Endpoint: strings.TrimPrefix(srv.URL, "http://"), AccessKey: "a", SecretKey: "b", Bucket: "bkt", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := c.GetRange(context.Background(), "blobs/x", 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "56789" || gotRange != "bytes=5-9" {
		t.Fatalf("body %q range %q", got, gotRange)
	}
}

// copyServer answers HEAD of the source with the given size and records every request that changes data.
func copyServer(t *testing.T, size int64, calls *[]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.Trim(r.URL.Path, "/")
		if !strings.Contains(path, "/") { // the bucket itself exists
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case r.Method == http.MethodHead:
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.Header().Set("Last-Modified", "Sat, 17 Oct 2026 10:00:00 GMT")
			w.Header().Set("ETag", `"abc"`)
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			*calls = append(*calls, "initiate")
			_, _ = w.Write([]byte(`<?xml version="1.0"?><InitiateMultipartUploadResult><Bucket>bkt</Bucket><Key>k</Key><UploadId>u1</UploadId></InitiateMultipartUploadResult>`))
		case r.Method == http.MethodPut && r.Header.Get("x-amz-copy-source") != "" && r.URL.Query().Has("partNumber"):
			*calls = append(*calls, "part")
			_, _ = w.Write([]byte(`<?xml version="1.0"?><CopyPartResult><ETag>"p"</ETag></CopyPartResult>`))
		case r.Method == http.MethodPut && r.Header.Get("x-amz-copy-source") != "":
			*calls = append(*calls, "copy")
			_, _ = w.Write([]byte(`<?xml version="1.0"?><CopyObjectResult><ETag>"c"</ETag></CopyObjectResult>`))
		case r.Method == http.MethodPost && r.URL.Query().Has("uploadId"):
			*calls = append(*calls, "complete")
			_, _ = w.Write([]byte(`<?xml version="1.0"?><CompleteMultipartUploadResult><Bucket>bkt</Bucket><Key>k</Key><ETag>"x"</ETag></CompleteMultipartUploadResult>`))
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: strings.TrimPrefix(srv.URL, "http://"), AccessKey: "a", SecretKey: "b", Bucket: "bkt", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// An object up to 5 GiB is copied by one request; a larger one by parts: S3 refuses a single copy over 5 GiB.
func TestCopyUsesPartsOnlyPastTheSingleCopyLimit(t *testing.T) {
	var small []string
	if err := copyServer(t, 1<<20, &small).Copy(context.Background(), "tmp/a", "blobs/a"); err != nil {
		t.Fatal(err)
	}
	if len(small) != 1 || small[0] != "copy" {
		t.Fatalf("small copy calls %v", small)
	}

	var large []string
	if err := copyServer(t, 6<<30, &large).Copy(context.Background(), "tmp/a", "blobs/a"); err != nil {
		t.Fatal(err)
	}
	if len(large) < 3 || large[0] != "initiate" || large[len(large)-1] != "complete" {
		t.Fatalf("large copy calls %v", large)
	}
}

// The deploy creates the bucket and its protection: the backend only checks that it is there, and never sends a request that
// creates it.
func TestNewDoesNotCreateAMissingBucket(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchBucket</Code><Message>no</Message></Error>`))
		}
	}))
	defer srv.Close()
	_, err := New(Config{Endpoint: strings.TrimPrefix(srv.URL, "http://"), AccessKey: "a", SecretKey: "b", Bucket: "bkt", Region: "us-east-1"})
	if !errors.Is(err, ErrBucketMissing) {
		t.Fatalf("want ErrBucketMissing, got %v", err)
	}
	for _, m := range methods {
		if m == http.MethodPut {
			t.Fatalf("the backend must not create the bucket: %v", methods)
		}
	}
}

// A delete is a plain one: on a versioned bucket it adds a delete marker and needs no right to delete a version, and the
// MinIO force-delete header (which needs a right of its own) is not sent.
func TestRemoveIsAPlainDelete(t *testing.T) {
	var gotHeaders http.Header
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.Trim(r.URL.Path, "/")
		if r.Method == http.MethodDelete {
			gotHeaders, gotQuery = r.Header.Clone(), r.URL.RawQuery
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !strings.Contains(path, "/") { // the bucket itself exists
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, err := New(Config{Endpoint: strings.TrimPrefix(srv.URL, "http://"), AccessKey: "a", SecretKey: "b", Bucket: "bkt", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Remove(context.Background(), "media/enc/x"); err != nil {
		t.Fatal(err)
	}
	if gotHeaders == nil {
		t.Fatal("no delete request was sent")
	}
	for name := range gotHeaders {
		if strings.Contains(strings.ToLower(name), "force-delete") {
			t.Fatalf("the force-delete header was sent: %s", name)
		}
	}
	if strings.Contains(gotQuery, "versionId") {
		t.Fatalf("a delete of a version was requested: %s", gotQuery)
	}
}
