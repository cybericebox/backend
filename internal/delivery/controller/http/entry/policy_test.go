package entry

import (
	"testing"

	"github.com/cybericebox/daemon/internal/config"
)

var testHosts = config.HostsConfig{
	Root: "example.test", API: "api.example.test", ID: "id.example.test", Admin: "admin.example.test",
	Exercises: "exercises.example.test", EventDomain: "example.test",
}

var allUpstreams = config.EntryConfig{
	Main: "main:80", ID: "id:80", Admin: "admin:80", Exercises: "ex:80", Event: "event:80",
}

func TestPolicyRoutes(t *testing.T) {
	p := NewPolicy(testHosts, allUpstreams)
	cases := []struct {
		name string
		want Route
	}{
		{"api.example.test", Route{Kind: Own}},
		{"example.test", Route{Kind: Forward, Upstream: "main:80"}},
		{"id.example.test", Route{Kind: Forward, Upstream: "id:80"}},
		{"admin.example.test", Route{Kind: Forward, Upstream: "admin:80"}},
		{"exercises.example.test", Route{Kind: Forward, Upstream: "ex:80"}},
		{"ctf2026.example.test", Route{Kind: Forward, Upstream: "event:80"}},
		// refused
		{"", Route{}},
		{"www.example.test", Route{}},
		{"a.b.example.test", Route{}},
		{"api.example.test.evil.test", Route{}},
		{"evil-example.test", Route{}},
		{"xexample.test", Route{}},
		{"example.test.evil.test", Route{}},
		{"localhost", Route{}},
		{"203.0.113.9", Route{}},
		{"example.com", Route{}},
		// the lab domain and the other laboratory labels are never passed on, even unreserved
		{"labs.example.test", Route{}},
		{"web-1.labs.example.test", Route{}},
		{"a.b.labs.example.test", Route{}},
		{"vpn.example.test", Route{}},
		{"ctl.example.test", Route{}},
	}
	for _, c := range cases {
		if got := p.Route(c.name); got != c.want {
			t.Errorf("Route(%q) = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestPolicyClassWithoutUpstreamIsRefused(t *testing.T) {
	p := NewPolicy(testHosts, config.EntryConfig{ID: "id:80"})
	if p.Route("id.example.test").Kind != Forward {
		t.Fatal("configured class")
	}
	for _, name := range []string{"example.test", "admin.example.test", "exercises.example.test", "ctf.example.test"} {
		if p.Route(name).Kind != Refuse {
			t.Errorf("%s: an unconfigured class must be refused", name)
		}
	}
	if p.Route("api.example.test").Kind != Own {
		t.Fatal("the own host does not depend on the upstreams")
	}
}

func TestPolicyDerivesFromTheDomainOnly(t *testing.T) {
	hosts := testHosts
	hosts.Root, hosts.EventDomain = "other.test", "other.test"
	hosts.API, hosts.ID = "api.other.test", "id.other.test"
	p := NewPolicy(hosts, allUpstreams)
	if p.Route("id.example.test").Kind != Refuse || p.Route("id.other.test").Kind != Forward {
		t.Fatal("the allow-list follows the hosts it was built from")
	}
	if NewPolicy(config.HostsConfig{}, allUpstreams).Route("anything.test").Kind != Refuse {
		t.Fatal("an empty configuration serves nothing")
	}
}

func TestPolicyReservedEventLabelsAreNotEventSites(t *testing.T) {
	hosts := testHosts
	hosts.ExtraReservedTags = []string{"staff"}
	p := NewPolicy(hosts, allUpstreams)
	if p.Route("staff.example.test").Kind != Refuse || p.Route("admin.example.test").Upstream == "event:80" {
		t.Fatal("a reserved label is not an event site")
	}
}
