// Package clientaddr is the one place that says which address a request comes from.
package clientaddr

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// cloudflareHeader carries the visitor's address when the connection comes through Cloudflare.
const cloudflareHeader = "CF-Connecting-IP"

// FromRequest is the address a request comes from, as a bare canonical address (IPv4-mapped IPv6 unmapped), or
// "" when it is not an IP address. It is the peer of the connection the request arrived on, with one exception:
// on a TLS connection whose client certificate was verified (authenticated origin pull: the peer is Cloudflare)
// the single CF-Connecting-IP value is the address, when it is exactly one IP address; a missing or malformed
// value keeps the peer. On any other connection no header is read, and no other forwarding header (X-Forwarded-For,
// Forwarded) and no PROXY protocol header is read at all, so a client can not choose its address. The certificate
// state is the request's own TLS session, so it holds for the in-process hand-off of the entry proxy and for
// HTTP/3 alike.
func FromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 {
		if values := r.Header.Values(cloudflareHeader); len(values) == 1 {
			if ip, err := netip.ParseAddr(strings.TrimSpace(values[0])); err == nil && ip.Zone() == "" {
				return ip.Unmap().String()
			}
		}
	}
	return FromAddr(r.RemoteAddr)
}

// FromAddr reads "host:port" (or a bare host) the way net.Conn.RemoteAddr().String() writes it.
func FromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" {
		return ""
	}
	return ip.Unmap().String()
}
