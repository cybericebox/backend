package config

import (
	"strings"
	"testing"
)

func TestEntryConfig_OffByDefaultAndValidated(t *testing.T) {
	if (EntryConfig{}).Enabled() {
		t.Fatal("the proxy mode is off by default")
	}
	tlsOn := HTTPServerTLSConfig{CertFile: "c", KeyFile: "k", MinVersion: "1.2", ClientAuth: "off"}
	cases := []struct {
		name    string
		cfg     HTTPServerConfig
		wantErr string
	}{
		{"off", HTTPServerConfig{TLS: tlsOn}, ""},
		{"on", HTTPServerConfig{TLS: tlsOn, Entry: EntryConfig{ID: "id.svc:8080", Event: "10.0.0.5:3000"}}, ""},
		{"needs TLS", HTTPServerConfig{TLS: HTTPServerTLSConfig{MinVersion: "1.2", ClientAuth: "off"}, Entry: EntryConfig{ID: "id:80"}}, "HTTP_SERVER_ENTRY_*"},
		{"no port", HTTPServerConfig{TLS: tlsOn, Entry: EntryConfig{Admin: "admin"}}, "HTTP_SERVER_ENTRY_ADMIN"},
		{"bad port", HTTPServerConfig{TLS: tlsOn, Entry: EntryConfig{Main: "main:http"}}, "not a port"},
		{"port zero", HTTPServerConfig{TLS: tlsOn, Entry: EntryConfig{Main: "main:0"}}, "not a port"},
		{"no host", HTTPServerConfig{TLS: tlsOn, Entry: EntryConfig{Exercises: ":80"}}, "HTTP_SERVER_ENTRY_EXERCISES"},
	}
	for _, c := range cases {
		err := c.cfg.validate()
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.wantErr)
		}
	}
}
