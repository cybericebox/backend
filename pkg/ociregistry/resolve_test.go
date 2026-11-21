package ociregistry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const resolveManifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`

// resolveServer serves one tag: headDigest is the Docker-Content-Digest of the HEAD ("" sends none), and it records
// the methods and the Accept header it saw.
type resolveServer struct {
	*httptest.Server
	mu      sync.Mutex
	methods []string
	accept  string
}

func newResolveServer(t *testing.T, headDigest string, head int) *resolveServer {
	t.Helper()
	rs := &resolveServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/manifests/1.2") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		rs.mu.Lock()
		rs.methods = append(rs.methods, r.Method)
		rs.accept = r.Header.Get("Accept")
		rs.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
		switch r.Method {
		case http.MethodHead:
			if headDigest != "" {
				w.Header().Set("Docker-Content-Digest", headDigest)
			}
			w.WriteHeader(head)
		case http.MethodGet:
			_, _ = w.Write([]byte(resolveManifest))
		}
	}))
	t.Cleanup(rs.Close)
	return rs
}

func TestResolveDigestUsesTheHeadAnswer(t *testing.T) {
	want := "sha256:" + strings.Repeat("a", 64)
	srv := newResolveServer(t, want, http.StatusOK)
	got, err := newClient(srv.Server, "", "").ResolveDigest(context.Background(), "1.2")
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, []string{http.MethodHead}, srv.methods, "a usable header needs no GET")
	for _, mediaType := range []string{
		"application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json",
	} {
		require.Contains(t, srv.accept, mediaType)
	}
}

func TestResolveDigestHashesTheBodyWithoutAUsableHeader(t *testing.T) {
	for name, header := range map[string]string{
		"no header":      "",
		"short digest":   "sha256:abc",
		"upper case hex": "sha256:" + strings.Repeat("A", 64),
		"other algo":     "sha512:" + strings.Repeat("a", 64),
	} {
		t.Run(name, func(t *testing.T) {
			srv := newResolveServer(t, header, http.StatusOK)
			got, err := newClient(srv.Server, "", "").ResolveDigest(context.Background(), "1.2")
			require.NoError(t, err)
			require.Equal(t, digestOf([]byte(resolveManifest)), got)
			require.Equal(t, []string{http.MethodHead, http.MethodGet}, srv.methods)
		})
	}
}

func TestResolveDigestFailures(t *testing.T) {
	srv := newResolveServer(t, "", http.StatusNotFound)
	c := newClient(srv.Server, "", "")
	_, err := c.ResolveDigest(context.Background(), "1.2")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = c.ResolveDigest(context.Background(), "no-such-tag")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = c.ResolveDigest(context.Background(), "bad tag/with slash")
	require.ErrorIs(t, err, ErrInvalidReference)

	denied := newResolveServer(t, "", http.StatusUnauthorized)
	_, err = newClient(denied.Server, "", "").ResolveDigest(context.Background(), "1.2")
	require.ErrorIs(t, err, ErrUnauthorized)
}

// A registry that sends no usable digest and answers the GET with something that is not a manifest (an HTML page of a proxy, a
// login wall) is not pinned: the hash of such a body would fix the image to a digest no registry serves.
func TestResolveDigestRefusesABodyThatIsNotAManifest(t *testing.T) {
	for name, body := range map[string]string{
		"html":          "<html>sign in</html>",
		"json no type":  `{"schemaVersion":2}`,
		"empty":         "",
		"unknown media": `{"mediaType":"text/plain"}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					return
				}
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(body))
				}
			}))
			t.Cleanup(srv.Close)
			got, err := newClient(srv, "", "").ResolveDigest(context.Background(), "1.2")
			require.ErrorIs(t, err, ErrUnsupportedManifest, "got %q", got)
		})
	}
}
