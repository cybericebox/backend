package entry

import (
	"strings"

	"github.com/cybericebox/daemon/internal/config"
)

// neverProxied are the labels under DOMAIN that belong to the laboratory (lab pages and VPN, the agent
// endpoint): no name at or under them is ever passed on, even when a deployment forgot to reserve the label.
var neverProxied = []string{"labs", "vpn", "ctl"}

// Kind says what to do with a server name.
type Kind int

const (
	// Refuse: not one of the platform's names, or a class with no upstream.
	Refuse Kind = iota
	// Own: the backend terminates TLS itself (the API host).
	Own
	// Forward: pass the TLS stream untouched to Route.Upstream.
	Forward
)

// Route is the decision for one server name.
type Route struct {
	Kind     Kind
	Upstream string
}

// Policy is the allow-list: computed once from the hosts (DOMAIN) and the configured upstreams, never changed and
// never extended by anything a client sends.
type Policy struct {
	hosts    config.HostsConfig
	exact    map[string]string // host -> upstream, for the named frontends
	event    string            // upstream of the event sites, "" when none
	own      string
	labNames []string
}

// NewPolicy builds the allow-list. hosts must be validated (every host derived from DOMAIN).
func NewPolicy(hosts config.HostsConfig, up config.EntryConfig) *Policy {
	p := &Policy{hosts: hosts, exact: map[string]string{}, event: up.Event, own: hosts.API}
	for host, upstream := range map[string]string{
		hosts.Root: up.Main, hosts.ID: up.ID, hosts.Admin: up.Admin, hosts.Exercises: up.Exercises,
	} {
		if host != "" && upstream != "" {
			p.exact[host] = upstream
		}
	}
	for _, label := range neverProxied {
		p.labNames = append(p.labNames, label+"."+hosts.Root)
	}
	return p
}

// Own is the one host the backend serves itself.
func (p *Policy) Own() string { return p.own }

// Route decides for a normalized (lower case) server name.
func (p *Policy) Route(name string) Route {
	if p.own == "" || name == "" {
		return Route{}
	}
	for _, lab := range p.labNames {
		if name == lab || strings.HasSuffix(name, "."+lab) {
			return Route{}
		}
	}
	if name == p.own {
		return Route{Kind: Own}
	}
	if upstream, ok := p.exact[name]; ok {
		return Route{Kind: Forward, Upstream: upstream}
	}
	if p.event != "" && p.hosts.IsPlatformHost(name) {
		if _, isEvent := p.hosts.EventTag(name); isEvent {
			return Route{Kind: Forward, Upstream: p.event}
		}
	}
	return Route{}
}
