// Package clientaddr is the one place that says which address a request comes from.
package clientaddr

import (
	"net"
	"net/http"
	"net/netip"
)

// FromRequest is the address of the peer of the connection the request arrived on, as a bare canonical address
// (IPv4-mapped IPv6 unmapped), or "" when it is not an IP address. The backend is the public entry: no
// forwarding header (X-Forwarded-For, Forwarded, CF-Connecting-IP) and no PROXY protocol header is read, so a
// client can not choose its address. HTTP/3 requests carry the QUIC peer in RemoteAddr, so they agree.
func FromRequest(r *http.Request) string {
	if r == nil {
		return ""
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
