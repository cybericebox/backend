package entry

import (
	"bytes"
	"errors"
	"io"
	"net"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cybericebox/daemon/internal/config"
)

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// upstream accepts connections, counts them and echoes everything back.
type upstream struct {
	ln       net.Listener
	accepted atomic.Int64
	received chan []byte
}

func startUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{ln: listen(t), received: make(chan []byte, 16)}
	go func() {
		for {
			c, err := u.ln.Accept()
			if err != nil {
				return
			}
			u.accepted.Add(1)
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				first := true
				for {
					n, err := c.Read(buf)
					if n > 0 {
						if first {
							u.received <- append([]byte(nil), buf[:n]...)
							first = false
						}
						_, _ = c.Write(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return u
}

func startProxy(t *testing.T, up config.EntryConfig, cfg Config) (*Proxy, string) {
	t.Helper()
	p := New(NewPolicy(testHosts, up), cfg)
	ln := listen(t)
	go func() { _ = p.Serve(ln) }()
	t.Cleanup(p.Close)
	return p, ln.Addr().String()
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

// expectClosed reads until the connection ends and returns what arrived first.
func expectClosed(t *testing.T, c net.Conn) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	data, err := io.ReadAll(c)
	// A close with unread input (a refused probe) may arrive as a reset: still a close.
	if err != nil && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("the connection was not closed: %v", err)
	}
	return data
}

func TestForwardPassesTheStreamUntouched(t *testing.T) {
	up := startUpstream(t)
	p, addr := startProxy(t, config.EntryConfig{ID: up.ln.Addr().String()}, Config{})
	hello := buildHello(helloOpts{names: []string{"id.example.test"}})
	c := dial(t, addr)
	if _, err := c.Write(append(append([]byte(nil), hello...), "after-hello"...)); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-up.received:
		if !bytes.HasPrefix(got, hello) {
			t.Fatal("the upstream did not receive the ClientHello byte for byte")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing reached the upstream")
	}
	want := len(hello) + len("after-hello")
	back := make([]byte, want)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, back); err != nil || !bytes.HasPrefix(back, hello) {
		t.Fatalf("the echo did not come back whole: %v", err)
	}
	_ = c.Close()
	eventually(t, "the forwarded slot is freed", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.forwarded == 0 && len(p.conns) == 0
	})
	if p.Stats.Forwarded.Load() != 1 {
		t.Fatalf("stats: %+v", p.Stats.Forwarded.Load())
	}
}

func TestUnknownOrUnconfiguredNamesNeverReachAnUpstream(t *testing.T) {
	up := startUpstream(t)
	p, addr := startProxy(t, config.EntryConfig{ID: up.ln.Addr().String(), Event: up.ln.Addr().String()}, Config{})
	for _, name := range []string{"evil.test", "admin.example.test", "labs.example.test", "x.labs.example.test", "www.example.test", "a.b.example.test", "203.0.113.9"} {
		c := dial(t, addr)
		_, _ = c.Write(buildHello(helloOpts{names: []string{name}}))
		if got := expectClosed(t, c); !bytes.Equal(got, tlsAlertUnrecognizedName) {
			t.Fatalf("%s: want the unrecognized_name alert, got %x", name, got)
		}
	}
	// A hello without a name gets the same answer.
	c := dial(t, addr)
	_, _ = c.Write(buildHello(helloOpts{extensions: otherExt()}))
	if got := expectClosed(t, c); !bytes.Equal(got, tlsAlertUnrecognizedName) {
		t.Fatalf("no SNI: %x", got)
	}
	if up.accepted.Load() != 0 {
		t.Fatal("an upstream was dialled for a refused name: open relay")
	}
	if p.Stats.Refused.Load() != 8 {
		t.Fatalf("refused = %d", p.Stats.Refused.Load())
	}
}

func TestNonTLSTrafficIsClosedSilentlyAndNeverForwarded(t *testing.T) {
	up := startUpstream(t)
	_, addr := startProxy(t, config.EntryConfig{ID: up.ln.Addr().String(), Event: up.ln.Addr().String()}, Config{})
	for _, in := range [][]byte{
		[]byte("GET / HTTP/1.1\r\nHost: id.example.test\r\n\r\n"),
		[]byte("CONNECT 127.0.0.1:22 HTTP/1.1\r\nHost: id.example.test\r\n\r\n"),
		{0x16, 3, 1, 0x40, 0x01}, // record over the TLS limit
		buildHello(helloOpts{names: []string{"a.example.test"}, declaredAdd: 5}), // hello longer than its record
	} {
		c := dial(t, addr)
		_, _ = c.Write(in)
		if got := expectClosed(t, c); len(got) != 0 {
			t.Fatalf("a non-TLS client got bytes: %q", got)
		}
	}
	if up.accepted.Load() != 0 {
		t.Fatal("an upstream was dialled")
	}
}

func TestSilentAndSlowClientsFreeTheirSlot(t *testing.T) {
	p, addr := startProxy(t, config.EntryConfig{}, Config{PeekTimeout: 150 * time.Millisecond})
	silent := dial(t, addr)
	// A client that sends the record header and then drips one byte at a time stays under the same deadline.
	slow := dial(t, addr)
	_, _ = slow.Write([]byte{0x16, 3, 1, 0x00, 0xff})
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(40 * time.Millisecond):
				if _, err := slow.Write([]byte{0}); err != nil {
					return
				}
			}
		}
	}()
	expectClosed(t, silent)
	expectClosed(t, slow)
	eventually(t, "no peeking slot is held", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.peeking == 0 && len(p.peekBySrc) == 0 && len(p.conns) == 0
	})
}

func TestPeekLimitsShedTheExcess(t *testing.T) {
	p, addr := startProxy(t, config.EntryConfig{}, Config{PeekTimeout: 5 * time.Second, MaxPeekingPerSource: 2, MaxPeeking: 10})
	held := []net.Conn{dial(t, addr), dial(t, addr)}
	eventually(t, "two peeking", func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.peeking == 2 })
	third := dial(t, addr)
	if got := expectClosed(t, third); len(got) != 0 {
		t.Fatal("the shed connection got bytes")
	}
	if p.Stats.Shed.Load() != 1 {
		t.Fatalf("shed = %d", p.Stats.Shed.Load())
	}
	// A slot freed by a closing client is usable again.
	_ = held[0].Close()
	eventually(t, "slot freed", func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.peeking == 1 })
	again := dial(t, addr)
	_, _ = again.Write(buildHello(helloOpts{names: []string{"evil.test"}}))
	if got := expectClosed(t, again); !bytes.Equal(got, tlsAlertUnrecognizedName) {
		t.Fatalf("after the slot was freed: %x", got)
	}
}

func TestForwardedConnectionsAreCapped(t *testing.T) {
	up := startUpstream(t)
	p, addr := startProxy(t, config.EntryConfig{ID: up.ln.Addr().String()}, Config{MaxForwarded: 2, MaxForwardedPerSource: 2})
	hello := buildHello(helloOpts{names: []string{"id.example.test"}})
	var open []net.Conn
	for i := 0; i < 2; i++ {
		c := dial(t, addr)
		_, _ = c.Write(hello)
		open = append(open, c)
	}
	eventually(t, "two forwarded", func() bool { return up.accepted.Load() == 2 })
	over := dial(t, addr)
	_, _ = over.Write(hello)
	expectClosed(t, over)
	if up.accepted.Load() != 2 || p.Stats.Shed.Load() != 1 {
		t.Fatalf("accepted=%d shed=%d", up.accepted.Load(), p.Stats.Shed.Load())
	}
	_ = open[0].Close()
	eventually(t, "a slot is free again", func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.forwarded == 1 })
}

func TestIdleForwardedConnectionIsClosedBothWays(t *testing.T) {
	up := startUpstream(t)
	_, addr := startProxy(t, config.EntryConfig{ID: up.ln.Addr().String()}, Config{IdleTimeout: 200 * time.Millisecond})
	c := dial(t, addr)
	_, _ = c.Write(buildHello(helloOpts{names: []string{"id.example.test"}}))
	<-up.received
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	_, _ = c.Read(buf) // the echo
	if _, err := c.Read(buf); err == nil {
		t.Fatal("expected the idle connection to end")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("the proxy did not close the idle connection")
	}
}

func TestUpstreamDownClosesTheClientWithoutALeak(t *testing.T) {
	ln := listen(t)
	dead := ln.Addr().String()
	_ = ln.Close()
	p, addr := startProxy(t, config.EntryConfig{ID: dead}, Config{DialTimeout: time.Second})
	c := dial(t, addr)
	_, _ = c.Write(buildHello(helloOpts{names: []string{"id.example.test"}}))
	expectClosed(t, c)
	eventually(t, "slots freed", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.forwarded == 0 && len(p.conns) == 0
	})
	if p.Stats.Failed.Load() != 1 {
		t.Fatal("the failure is not counted")
	}
}

func TestOwnNamesAreHandedToTheBackendWithTheHelloReplayed(t *testing.T) {
	p, addr := startProxy(t, config.EntryConfig{}, Config{})
	hello := buildHello(helloOpts{names: []string{"api.example.test"}})
	c := dial(t, addr)
	_, _ = c.Write(append(append([]byte(nil), hello...), "more"...))
	accepted, err := p.OwnListener().Accept()
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(hello)+4)
	_ = accepted.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err = io.ReadFull(accepted, got); err != nil || !bytes.Equal(got, append(append([]byte(nil), hello...), "more"...)) {
		t.Fatalf("the replay is not whole: %v", err)
	}
	if accepted.RemoteAddr().String() != c.LocalAddr().String() {
		t.Fatalf("the client address must stay the peer's: %s vs %s", accepted.RemoteAddr(), c.LocalAddr())
	}
	_ = accepted.Close()
	eventually(t, "the connection is forgotten", func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.conns) == 0 })
}

func TestCloseEndsEverythingAndLeaksNothing(t *testing.T) {
	before := runtime.NumGoroutine()
	up := startUpstream(t)
	p := New(NewPolicy(testHosts, config.EntryConfig{ID: up.ln.Addr().String()}), Config{})
	ln := listen(t)
	served := make(chan struct{})
	go func() { _ = p.Serve(ln); close(served) }()
	var clients []net.Conn
	// one silent, one forwarded, one waiting in the handoff queue
	clients = append(clients, dial(t, ln.Addr().String()))
	fw := dial(t, ln.Addr().String())
	_, _ = fw.Write(buildHello(helloOpts{names: []string{"id.example.test"}}))
	clients = append(clients, fw)
	own := dial(t, ln.Addr().String())
	_, _ = own.Write(buildHello(helloOpts{names: []string{"api.example.test"}}))
	clients = append(clients, own)
	eventually(t, "all three are in", func() bool { return up.accepted.Load() == 1 && p.Stats.Own.Load() == 1 })

	p.Close()
	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return")
	}
	for _, c := range clients {
		expectClosed(t, c)
	}
	if _, err := p.OwnListener().Accept(); err == nil {
		t.Fatal("the own listener must be closed")
	}
	eventually(t, "goroutines are back", func() bool { return runtime.NumGoroutine() <= before+4 })
}

func TestSourceKeyGroupsAnIPv6SubscriberTogether(t *testing.T) {
	a := sourceKey(&net.TCPAddr{IP: net.ParseIP("2001:db8:1:2::1")})
	b := sourceKey(&net.TCPAddr{IP: net.ParseIP("2001:db8:1:2:ffff::9")})
	c := sourceKey(&net.TCPAddr{IP: net.ParseIP("2001:db8:1:3::1")})
	if a != b || a == c {
		t.Fatalf("a=%v b=%v c=%v", a, b, c)
	}
	if sourceKey(&net.TCPAddr{IP: net.ParseIP("::ffff:203.0.113.9")}) != sourceKey(&net.TCPAddr{IP: net.ParseIP("203.0.113.9")}) {
		t.Fatal("mapped v4")
	}
}
