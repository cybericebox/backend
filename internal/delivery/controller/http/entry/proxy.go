package entry

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// Limits (the public-entry posture). Memory is bounded by them: a peeking connection holds at most one TLS record
// (16 KiB), a passed connection two 16 KiB copy buffers.
const (
	// DefaultPeekTimeout bounds the whole wait for the first record, so a silent or drip-feeding client frees its
	// slot after this long, whatever it sends.
	DefaultPeekTimeout = 5 * time.Second
	// DefaultDialTimeout bounds the connection to an upstream.
	DefaultDialTimeout = 5 * time.Second
	// DefaultIdleTimeout closes a passed connection that moves no byte in either direction for this long (kept
	// under the load balancer's 350 s idle timeout).
	DefaultIdleTimeout = 300 * time.Second
	// DefaultMaxPeeking is the connections waiting for their first record, in total.
	DefaultMaxPeeking = 1024
	// DefaultMaxPeekingPerSource is the same for one source (an IPv4 address or an IPv6 /64); a venue behind one
	// NAT opens many connections at once, so it is generous.
	DefaultMaxPeekingPerSource = 128
	// DefaultMaxForwarded is the connections passed through to upstreams at once, in total.
	DefaultMaxForwarded = 4096
	// DefaultMaxForwardedPerSource is the same for one source.
	DefaultMaxForwardedPerSource = 1024
	// handoffBacklog is the connections waiting for the HTTPS server to accept them.
	handoffBacklog    = 128
	alertWriteTimeout = time.Second
	copyBufferSize    = 16 << 10
)

// tlsAlertUnrecognizedName is a fatal unrecognized_name alert (TLS 1.0-1.3 record layer, version 1.2).
var tlsAlertUnrecognizedName = []byte{0x15, 0x03, 0x03, 0x00, 0x02, 0x02, 0x70}

// Config tunes a Proxy; a zero field takes its default.
type Config struct {
	PeekTimeout, DialTimeout, IdleTimeout time.Duration
	MaxPeeking, MaxPeekingPerSource       int
	MaxForwarded, MaxForwardedPerSource   int
}

func (c *Config) defaults() {
	if c.PeekTimeout <= 0 {
		c.PeekTimeout = DefaultPeekTimeout
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = DefaultDialTimeout
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultIdleTimeout
	}
	if c.MaxPeeking <= 0 {
		c.MaxPeeking = DefaultMaxPeeking
	}
	if c.MaxPeekingPerSource <= 0 {
		c.MaxPeekingPerSource = DefaultMaxPeekingPerSource
	}
	if c.MaxForwarded <= 0 {
		c.MaxForwarded = DefaultMaxForwarded
	}
	if c.MaxForwardedPerSource <= 0 {
		c.MaxForwardedPerSource = DefaultMaxForwardedPerSource
	}
}

// Stats counts what happened to connections, for tests and the log line at shutdown.
type Stats struct {
	Accepted, Own, Forwarded, Refused, Shed, Failed atomic.Int64
}

// Proxy routes the connections of one listener by server name.
type Proxy struct {
	policy *Policy
	cfg    Config
	own    *handoff
	Stats  Stats

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	peeking   int
	forwarded int
	peekBySrc map[netip.Addr]int
	fwdBySrc  map[netip.Addr]int
	conns     map[net.Conn]struct{} // every connection under the proxy's care, so Close can end them
	closed    bool
	listeners []net.Listener
}

// New builds a proxy for policy.
func New(policy *Policy, cfg Config) *Proxy {
	cfg.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	return &Proxy{
		policy: policy, cfg: cfg, ctx: ctx, cancel: cancel,
		own:       newHandoff(),
		peekBySrc: map[netip.Addr]int{}, fwdBySrc: map[netip.Addr]int{},
		conns: map[net.Conn]struct{}{},
	}
}

// OwnListener is the listener the HTTPS server of the backend serves: it yields the connections whose server name
// is the backend's own host, with the peeked bytes replayed in front of the rest of the stream.
func (p *Proxy) OwnListener() net.Listener { return p.own }

// sourceKey groups a peer: an IPv4 address, or the /64 of an IPv6 address (one subscriber owns a whole /64).
func sourceKey(addr net.Addr) netip.Addr {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return netip.Addr{}
	}
	ip, ok := netip.AddrFromSlice(tcp.IP)
	if !ok {
		return netip.Addr{}
	}
	ip = ip.Unmap()
	if ip.Is6() {
		prefix, _ := ip.Prefix(64)
		return prefix.Addr()
	}
	return ip
}

// Serve accepts on ln until it is closed. It never blocks on a connection: each one gets a goroutine only after
// the total and per-source peek limits admit it, and is dropped at once otherwise.
func (p *Proxy) Serve(ln net.Listener) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = ln.Close()
		return net.ErrClosed
	}
	p.listeners = append(p.listeners, ln)
	p.mu.Unlock()
	var delay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if p.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return net.ErrClosed
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if delay == 0 {
					delay = 5 * time.Millisecond
				} else if delay *= 2; delay > time.Second {
					delay = time.Second
				}
				select {
				case <-time.After(delay):
					continue
				case <-p.ctx.Done():
					return net.ErrClosed
				}
			}
			return err
		}
		delay = 0
		p.Stats.Accepted.Add(1)
		src := sourceKey(conn.RemoteAddr())
		if !p.admitPeek(src, conn) {
			p.Stats.Shed.Add(1)
			_ = conn.Close()
			continue
		}
		p.wg.Add(1)
		go p.handle(conn, src)
	}
}

func (p *Proxy) admitPeek(src netip.Addr, conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.peeking >= p.cfg.MaxPeeking || p.peekBySrc[src] >= p.cfg.MaxPeekingPerSource {
		return false
	}
	p.peeking++
	p.peekBySrc[src]++
	p.conns[conn] = struct{}{}
	return true
}

func (p *Proxy) endPeek(src netip.Addr) {
	p.mu.Lock()
	p.peeking--
	if p.peekBySrc[src]--; p.peekBySrc[src] <= 0 {
		delete(p.peekBySrc, src)
	}
	p.mu.Unlock()
}

func (p *Proxy) admitForward(src netip.Addr) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.forwarded >= p.cfg.MaxForwarded || p.fwdBySrc[src] >= p.cfg.MaxForwardedPerSource {
		return false
	}
	p.forwarded++
	p.fwdBySrc[src]++
	return true
}

func (p *Proxy) endForward(src netip.Addr) {
	p.mu.Lock()
	p.forwarded--
	if p.fwdBySrc[src]--; p.fwdBySrc[src] <= 0 {
		delete(p.fwdBySrc, src)
	}
	p.mu.Unlock()
}

func (p *Proxy) forget(conn net.Conn) {
	p.mu.Lock()
	delete(p.conns, conn)
	p.mu.Unlock()
}

// handle peeks, decides and either hands the connection to the backend or passes it through.
func (p *Proxy) handle(conn net.Conn, src netip.Addr) {
	defer p.wg.Done()
	handedOff := false
	defer func() {
		if !handedOff {
			p.forget(conn)
			_ = conn.Close()
		}
	}()

	// One absolute deadline for the whole peek: a drip-fed hello ends when it passes, like a silent client.
	_ = conn.SetReadDeadline(time.Now().Add(p.cfg.PeekTimeout))
	name, raw, err := ReadClientHello(conn)
	p.endPeek(src)
	if err != nil {
		p.refuse(conn, err)
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	route := p.policy.Route(name)
	switch route.Kind {
	case Own:
		if p.own.offer(&replayConn{Conn: conn, prefix: raw, release: func() { p.forget(conn) }}) {
			handedOff = true
			p.Stats.Own.Add(1)
			return
		}
		p.Stats.Shed.Add(1)
	case Forward:
		if !p.admitForward(src) {
			p.Stats.Shed.Add(1)
			return
		}
		defer p.endForward(src)
		p.forward(conn, raw, route.Upstream)
	default:
		p.refuse(conn, ErrBadServerName)
	}
}

// refuse ends a connection that is not for us. Only a well-formed TLS client that asked for a name we do not
// serve (or sent none) gets an alert; anything else, a probe, a port scan, plain HTTP, is closed without a word.
func (p *Proxy) refuse(conn net.Conn, reason error) {
	p.Stats.Refused.Add(1)
	if errors.Is(reason, ErrBadServerName) || errors.Is(reason, ErrNoServerName) {
		_ = conn.SetWriteDeadline(time.Now().Add(alertWriteTimeout))
		_, _ = conn.Write(tlsAlertUnrecognizedName)
	}
	log.Debug().Err(reason).Str("peer", conn.RemoteAddr().String()).Msg("Entry: connection refused")
}

// forward connects to the fixed upstream, replays the peeked bytes and copies both ways until both sides end or
// the idle timeout passes. Nothing is read or changed in the stream.
func (p *Proxy) forward(client net.Conn, raw []byte, upstream string) {
	dialer := net.Dialer{Timeout: p.cfg.DialTimeout, KeepAlive: 30 * time.Second}
	up, err := dialer.DialContext(p.ctx, "tcp", upstream)
	if err != nil {
		p.Stats.Failed.Add(1)
		log.Warn().Err(err).Str("upstream", upstream).Msg("Entry: can not reach the upstream")
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = up.Close()
		return
	}
	p.conns[up] = struct{}{}
	p.mu.Unlock()
	defer func() { p.forget(up); _ = up.Close() }()

	_ = up.SetWriteDeadline(time.Now().Add(p.cfg.IdleTimeout))
	if _, err = up.Write(raw); err != nil {
		p.Stats.Failed.Add(1)
		return
	}
	p.Stats.Forwarded.Add(1)

	done := make(chan struct{}, 2)
	go func() { pipe(up, client, p.cfg.IdleTimeout); done <- struct{}{} }()
	go func() { pipe(client, up, p.cfg.IdleTimeout); done <- struct{}{} }()
	<-done
	<-done
}

var bufPool = sync.Pool{New: func() any { b := make([]byte, copyBufferSize); return &b }}

// pipe copies src to dst; every read and write has its own idle deadline. When src ends, dst gets a half close so
// the other direction can finish; on an error both sides are closed by the caller after the second pipe ends, and
// the deadline guarantees it does.
func pipe(dst, src net.Conn, idle time.Duration) {
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	buf := *bp
	for {
		_ = src.SetReadDeadline(time.Now().Add(idle))
		n, err := src.Read(buf)
		if n > 0 {
			_ = dst.SetWriteDeadline(time.Now().Add(idle))
			if _, werr := dst.Write(buf[:n]); werr != nil {
				_ = src.Close()
				_ = dst.Close()
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if cw, ok := dst.(interface{ CloseWrite() error }); ok {
					_ = cw.CloseWrite()
					return
				}
			}
			_ = src.Close()
			_ = dst.Close()
			return
		}
	}
}

// Close stops accepting, ends every connection under the proxy's care and waits for the goroutines.
func (p *Proxy) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	listeners := p.listeners
	conns := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	p.cancel()
	for _, ln := range listeners {
		_ = ln.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	_ = p.own.Close()
	p.wg.Wait()
}

// handoff is the in-process listener the HTTPS server accepts the backend's own connections from.
type handoff struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newHandoff() *handoff {
	return &handoff{ch: make(chan net.Conn, handoffBacklog), done: make(chan struct{})}
}

// offer queues a connection without blocking; false (full or closed) means the caller drops it.
func (h *handoff) offer(c net.Conn) bool {
	select {
	case <-h.done:
		return false
	default:
	}
	select {
	case h.ch <- c:
		return true
	default:
		return false
	}
}

func (h *handoff) Accept() (net.Conn, error) {
	select {
	case c := <-h.ch:
		return c, nil
	case <-h.done:
		return nil, net.ErrClosed
	}
}

func (h *handoff) Close() error {
	h.once.Do(func() {
		close(h.done)
		for {
			select {
			case c := <-h.ch:
				_ = c.Close()
			default:
				return
			}
		}
	})
	return nil
}

func (h *handoff) Addr() net.Addr { return handoffAddr{} }

type handoffAddr struct{}

func (handoffAddr) Network() string { return "entry" }
func (handoffAddr) String() string  { return "entry" }

// replayConn gives back the peeked bytes before the rest of the stream; RemoteAddr stays the real peer's.
type replayConn struct {
	net.Conn

	prefix  []byte
	release func()
	once    sync.Once
}

func (c *replayConn) Read(b []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(b, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(b)
}

func (c *replayConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}
