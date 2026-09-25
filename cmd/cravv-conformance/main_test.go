package main

import (
	"flag"
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
		{"double dash flags", []string{"--relay", "http://127.0.0.1:8787", "--admin-token", "t"}, "", options{relay: "http://127.0.0.1:8787", adminToken: "t"}, false},
		{"env token and slow", []string{"-relay=https://r.example", "-slow"}, "e", options{relay: "https://r.example", adminToken: "e", slow: true}, false},
		{"missing relay", []string{"--admin-token", "t"}, "", options{}, true},
		{"missing token", []string{"--relay", "http://x"}, "", options{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			got, err := parseOptions(fs, tc.args, env(tc.env))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
