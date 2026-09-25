package main

import (
	"io"
	"testing"
)

func TestParseOptions(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "CRAVV_RELAY_ADMIN_TOKEN" {
				return v
			}
			return ""
		}
	}
	tests := []struct {
		name    string
		args    []string
		env     string
		want    options
		wantErr bool
	}{
		{"flag token", []string{"-admin-token", "t1"}, "", options{addr: "127.0.0.1:8787", origin: "http://127.0.0.1:8787", adminToken: "t1"}, false},
		{"env token", nil, "t2", options{addr: "127.0.0.1:8787", origin: "http://127.0.0.1:8787", adminToken: "t2"}, false},
		{"flag wins over env", []string{"-admin-token", "t1"}, "t2", options{addr: "127.0.0.1:8787", origin: "http://127.0.0.1:8787", adminToken: "t1"}, false},
		{"explicit origin", []string{"-addr", ":9000", "-origin", "https://relay.example.com", "-admin-token", "x"}, "", options{addr: ":9000", origin: "https://relay.example.com", adminToken: "x"}, false},
		{"origin normalized", []string{"-origin", "HTTPS://Relay.Example.com:443/", "-admin-token", "x"}, "", options{addr: "127.0.0.1:8787", origin: "https://relay.example.com", adminToken: "x"}, false},
		{"default origin for wildcard addr", []string{"-addr", ":9000", "-admin-token", "x"}, "", options{addr: ":9000", origin: "http://127.0.0.1:9000", adminToken: "x"}, false},
		{"bad origin", []string{"-origin", "https://relay.example.com/path", "-admin-token", "x"}, "", options{}, true},
		{"no token", nil, "", options{}, true},
		{"bad flag", []string{"-nope"}, "t", options{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOptions(tc.args, env(tc.env), io.Discard)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
