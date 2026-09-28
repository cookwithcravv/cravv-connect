package main

import (
	"flag"
	"io"
	"strings"
	"testing"
)

func TestParseOptions(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		stdin   string
		want    options
		wantErr bool
	}{
		{"double dash flags", []string{"--relay", "http://127.0.0.1:8787", "--admin-token", "t"}, nil, "", options{relay: "http://127.0.0.1:8787", adminToken: "t"}, false},
		{"legacy env token and slow", []string{"-relay=https://r.example", "-slow"}, map[string]string{"CRAVV_RELAY_ADMIN_TOKEN": "e"}, "", options{relay: "https://r.example", adminToken: "e", slow: true}, false},
		{"conformance env token", []string{"--relay", "http://x"}, map[string]string{"CRAVV_CONFORMANCE_ADMIN_TOKEN": "c", "CRAVV_RELAY_ADMIN_TOKEN": "e"}, "", options{relay: "http://x", adminToken: "c"}, false},
		{"flag wins over env", []string{"--relay", "http://x", "--admin-token", "t"}, map[string]string{"CRAVV_CONFORMANCE_ADMIN_TOKEN": "c"}, "", options{relay: "http://x", adminToken: "t"}, false},
		{"token from stdin", []string{"--relay", "http://x", "--admin-token", "-"}, map[string]string{"CRAVV_CONFORMANCE_ADMIN_TOKEN": "c"}, "  s3cret \nrest\n", options{relay: "http://x", adminToken: "s3cret"}, false},
		{"stdin without newline", []string{"--relay", "http://x", "--admin-token=-"}, nil, "s3cret", options{relay: "http://x", adminToken: "s3cret"}, false},
		{"empty stdin is an error even with env", []string{"--relay", "http://x", "--admin-token", "-"}, map[string]string{"CRAVV_CONFORMANCE_ADMIN_TOKEN": "c"}, "", options{}, true},
		{"blank stdin line", []string{"--relay", "http://x", "--admin-token", "-"}, nil, "  \n", options{}, true},
		{"missing relay", []string{"--admin-token", "t"}, nil, "", options{}, true},
		{"missing token", []string{"--relay", "http://x"}, nil, "", options{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			got, err := parseOptions(fs, tc.args, env(tc.env), strings.NewReader(tc.stdin))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
