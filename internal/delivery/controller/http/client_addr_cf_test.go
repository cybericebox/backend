package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/quic-go/quic-go/http3"

	"github.com/cybericebox/backend/internal/config"
	"github.com/cybericebox/backend/internal/delivery/controller/http/clientaddr"
)

// cfStand is a Server in TLS mode (directly, or behind the entry proxy) whose /ip route answers with
// clientaddr.FromRequest; the client certificate comes from the CA the server trusts for client auth.
type cfStand struct {
	port       string
	pool       *x509.CertPool
	clientCert *tls.Certificate
}

func startCF(t *testing.T, clientAuth string, withEntry, withH3 bool) *cfStand {
	t.Helper()
	gin.SetMode(gin.TestMode)
	serverCA, clientCA := newTestCA(t, "server-ca"), newTestCA(t, "aop-ca")
	dir := t.TempDir()
	certPEM, keyPEM := serverCA.issue(t, "localhost", true)
	write(t, filepath.Join(dir, "tls.crt"), certPEM)
	write(t, filepath.Join(dir, "tls.key"), keyPEM)
	write(t, filepath.Join(dir, "ca.crt"), clientCA.pem)
	st := &cfStand{pool: x509.NewCertPool(), clientCert: clientCertFrom(t, clientCA), port: freePort(t)}
	st.pool.AddCert(serverCA.cert)

	cfg := config.HTTPServerConfig{
		Host: "127.0.0.1", HTTPSPort: st.port,
		ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderMegabytes: 1,
		TLS: config.HTTPServerTLSConfig{
			ClientAuth: clientAuth, MinVersion: "1.2",
			CertFile: filepath.Join(dir, "tls.crt"), KeyFile: filepath.Join(dir, "tls.key"), ClientCAFile: filepath.Join(dir, "ca.crt"),
		},
		HTTP3: config.HTTP3Config{Enabled: withH3, AdvertisedPort: "443"},
	}
	if withEntry {
		cfg.Entry = config.EntryConfig{ID: "127.0.0.1:9"}
	}
	router := gin.New()
	router.GET("/ip", func(c *gin.Context) { c.String(http.StatusOK, clientaddr.FromRequest(c.Request)) })
	srv := NewServer(&cfg, "127.0.0.1", router, entryHosts)
	srv.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Stop(ctx)
	})
	waitListening(t, "127.0.0.1:"+st.port)
	return st
}

func (st *cfStand) tlsConfig(withCert bool) *tls.Config {
	cfg := &tls.Config{RootCAs: st.pool, ServerName: "localhost"}
	if withCert {
		cfg.Certificates = []tls.Certificate{*st.clientCert}
	}
	return cfg
}

// ipOver asks /ip over the given transport with the CF-Connecting-IP header set to header (when not empty).
func ipOver(t *testing.T, rt http.RoundTripper, port, header string) string {
	t.Helper()
	var lastErr error
	for range 20 {
		req, _ := http.NewRequest(http.MethodGet, "https://127.0.0.1:"+port+"/ip", nil)
		if header != "" {
			req.Header.Set("CF-Connecting-IP", header)
		}
		resp, err := (&http.Client{Transport: rt, Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(body)
	}
	t.Fatal(lastErr)
	return ""
}

func TestCloudflareHeaderOnlyOnAVerifiedCertificateConnection(t *testing.T) {
	for _, mode := range []struct {
		name           string
		entry, h3      bool
		useH3          bool
		trustsWithCert bool
	}{
		{"direct", false, false, false, true},
		{"entry hand-off", true, false, false, true},
		{"http3", false, true, true, true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			st := startCF(t, "optional", mode.entry, mode.h3)
			rt := func(withCert bool) http.RoundTripper {
				if mode.useH3 {
					tr := &http3.Transport{TLSClientConfig: st.tlsConfig(withCert)}
					t.Cleanup(func() { _ = tr.Close() })
					return tr
				}
				return &http.Transport{TLSClientConfig: st.tlsConfig(withCert), ForceAttemptHTTP2: true}
			}
			for header, want := range map[string]string{
				"198.51.100.7":           "198.51.100.7",
				" 198.51.100.7 ":         "198.51.100.7",
				"2001:db8::1":            "2001:db8::1",
				"::ffff:198.51.100.7":    "198.51.100.7",
				"":                       "127.0.0.1",
				"not-an-ip":              "127.0.0.1",
				"198.51.100.7, 10.0.0.1": "127.0.0.1",
				"198.51.100.7:443":       "127.0.0.1",
				"[2001:db8::1]:443":      "127.0.0.1",
				"fe80::1%eth0":           "127.0.0.1",
			} {
				if got := ipOver(t, rt(true), st.port, header); got != want {
					t.Errorf("verified certificate, header %q: got %q want %q", header, got, want)
				}
			}
			// A forged header on a connection with no client certificate is never read.
			if got := ipOver(t, rt(false), st.port, "198.51.100.7"); got != "127.0.0.1" {
				t.Errorf("no certificate: forged header was used: %q", got)
			}
		})
	}
}

func TestCloudflareHeaderIgnoredWhenClientAuthIsOff(t *testing.T) {
	st := startCF(t, "off", false, false)
	rt := &http.Transport{TLSClientConfig: st.tlsConfig(true), ForceAttemptHTTP2: true}
	if got := ipOver(t, rt, st.port, "198.51.100.7"); got != "127.0.0.1" {
		t.Fatalf("got %q", got)
	}
}
