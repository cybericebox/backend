package entry

import (
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cybericebox/daemon/internal/config"
)

// flakyListener fails the first accepts with a non-timeout error (file descriptors exhausted), then works.
type flakyListener struct {
	net.Listener
	fails atomic.Int32
}

func (l *flakyListener) Accept() (net.Conn, error) {
	if l.fails.Add(-1) >= 0 {
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: syscall.EMFILE}
	}
	return l.Listener.Accept()
}

func TestServeSurvivesATransientAcceptError(t *testing.T) {
	up := startUpstream(t)
	p := New(NewPolicy(testHosts, config.EntryConfig{ID: up.ln.Addr().String()}), Config{})
	base := listen(t)
	ln := &flakyListener{Listener: base}
	ln.fails.Store(1)
	done := make(chan error, 1)
	go func() { done <- p.Serve(ln) }()
	t.Cleanup(p.Close)
	select {
	case err := <-done:
		t.Fatalf("Serve gave up on a transient accept error (the port would go dark): %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	c := dial(t, base.Addr().String())
	_, _ = c.Write(buildHello(helloOpts{names: []string{"id.example.test"}}))
	select {
	case <-up.received:
	case <-time.After(3 * time.Second):
		t.Fatal("the proxy does not serve after an accept error")
	}
}

// A passed connection that only receives (a long download, the client sends nothing) is not idle.
func TestForwardedDownloadIsNotIdle(t *testing.T) {
	ln := listen(t)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 4096)
		_, _ = c.Read(buf)
		for i := 0; i < 30; i++ {
			if _, err := c.Write([]byte("x")); err != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	_, addr := startProxy(t, config.EntryConfig{ID: ln.Addr().String()}, Config{IdleTimeout: 300 * time.Millisecond})
	c := dial(t, addr)
	_, _ = c.Write(buildHello(helloOpts{names: []string{"id.example.test"}}))
	got := 0
	buf := make([]byte, 64)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		n, err := c.Read(buf)
		got += n
		if err != nil {
			break
		}
	}
	// the hello is echoed? no: the upstream above does not echo; it sends 30 bytes over 1.5 s.
	if got != 30 {
		t.Fatalf("the download was cut after %d of 30 bytes: the idle timeout counts one direction only", got)
	}
}
