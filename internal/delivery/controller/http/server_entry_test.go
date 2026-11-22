package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/quic-go/quic-go/http3"

	"github.com/cybericebox/backend/internal/config"
	"github.com/cybericebox/backend/internal/delivery/controller/http/clientaddr"
)

// entryHosts: the API host is localhost so the test certificate (localhost) matches the server name.
var entryHosts = config.HostsConfig{
	Root: "example.test", API: "localhost", ID: "id.example.test", Admin: "admin.example.test",
	Exercises: "exercises.example.test", EventDomain: "example.test",
}

type entryStand struct {
	port     string
	pool     *x509.CertPool
	upstream net.Listener
	hellos   chan byte
}

func startEntry(t *testing.T, withH3 bool, upstreams func(addr string) config.EntryConfig) *entryStand {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ca := newTestCA(t, "server-ca")
	dir := t.TempDir()
	certPEM, keyPEM := ca.issue(t, "localhost", true)
	write(t, filepath.Join(dir, "tls.crt"), certPEM)
	write(t, filepath.Join(dir, "tls.key"), keyPEM)
	st := &entryStand{pool: x509.NewCertPool(), hellos: make(chan byte, 4)}
	st.pool.AddCert(ca.cert)

	var err error
	if st.upstream, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.upstream.Close() })
	go func() {
		for {
			c, acceptErr := st.upstream.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer c.Close()
				b := make([]byte, 1)
				if _, readErr := io.ReadFull(c, b); readErr == nil {
					st.hellos <- b[0]
				}
			}()
		}
	}()

	st.port = freePort(t)
	cfg := config.HTTPServerConfig{
		Host: "127.0.0.1", HTTPSPort: st.port,
		ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderMegabytes: 1,
		TLS: config.HTTPServerTLSConfig{
			ClientAuth: "off", MinVersion: "1.2",
			CertFile: filepath.Join(dir, "tls.crt"), KeyFile: filepath.Join(dir, "tls.key"),
		},
		HTTP3: config.HTTP3Config{Enabled: withH3, AdvertisedPort: "443"},
		Entry: upstreams(st.upstream.Addr().String()),
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

func TestEntry_OwnHostIsServedWithTheDirectClientAddress(t *testing.T) {
	st := startEntry(t, false, func(addr string) config.EntryConfig { return config.EntryConfig{ID: addr} })
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: st.pool, ServerName: "localhost"}},
	}
	resp, err := client.Get("https://127.0.0.1:" + st.port + "/ip")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "127.0.0.1" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}

func TestEntry_FrontendHostIsPassedThroughAndOthersAreRefused(t *testing.T) {
	st := startEntry(t, false, func(addr string) config.EntryConfig { return config.EntryConfig{ID: addr} })
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", "127.0.0.1:"+st.port,
		&tls.Config{ServerName: "id.example.test", RootCAs: st.pool})
	if err == nil {
		_ = conn.Close()
		t.Fatal("the upstream is not TLS: the handshake can not succeed")
	}
	select {
	case first := <-st.hellos:
		if first != 0x16 {
			t.Fatalf("the upstream got %#x first, want the ClientHello record", first)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the TLS stream did not reach the frontend upstream")
	}
	// A name that is not the platform's gets an alert; the upstream sees nothing more.
	for _, name := range []string{"evil.test", "admin.example.test", "labs.example.test"} {
		c, dialErr := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", "127.0.0.1:"+st.port,
			&tls.Config{ServerName: name, RootCAs: st.pool})
		if dialErr == nil {
			_ = c.Close()
			t.Fatalf("%s: handshake succeeded", name)
		}
	}
	select {
	case first := <-st.hellos:
		t.Fatalf("a refused name reached the upstream (%#x)", first)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestEntry_HTTP3ServesOnlyTheOwnHost(t *testing.T) {
	st := startEntry(t, true, func(addr string) config.EntryConfig { return config.EntryConfig{ID: addr} })
	tr := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: st.pool, ServerName: "localhost"}}
	defer tr.Close()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	var resp *http.Response
	var err error
	for i := 0; i < 20; i++ {
		if resp, err = client.Get("https://127.0.0.1:" + st.port + "/ip"); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.ProtoMajor != 3 || string(body) != "127.0.0.1" {
		t.Fatalf("proto %d body %q", resp.ProtoMajor, body)
	}
}

func TestOnlyServerNameRefusesEveryOtherName(t *testing.T) {
	cfg := &tls.Config{}
	onlyServerName(cfg, "api.example.test")
	for name, ok := range map[string]bool{
		"api.example.test": true, "API.example.test": true, "": false, "id.example.test": false,
		"api.example.test.evil.test": false, "example.test": false,
	} {
		_, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{ServerName: name})
		if (err == nil) != ok {
			t.Errorf("%q: err=%v, want ok=%v", name, err, ok)
		}
	}
}
