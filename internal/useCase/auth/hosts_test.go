package auth_test

import "github.com/cybericebox/backend/internal/config"

// testHosts is a full host set under domain.
func testHosts(domain string) config.HostsConfig {
	return config.HostsConfig{
		Root: domain, API: "api." + domain, ID: "id." + domain,
		Admin: "admin." + domain, Exercises: "exercises." + domain, EventDomain: domain,
	}
}
