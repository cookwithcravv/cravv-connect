package scripts

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The README's quick start installs with the same one-liner install.sh
// documents, then uses setup and setup --join; it has no em dashes.
func TestReadmeQuickStart(t *testing.T) {
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	oneLiner := regexp.MustCompile(`(?m)^#   (curl -fsSL \S+/scripts/install\.sh \| sh)$`).FindSubmatch(script)
	if oneLiner == nil {
		t.Fatal("install.sh documents no one-liner")
	}
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	quick := string(readme)
	for _, want := range []string{string(oneLiner[1]), "\ncravv-connect setup\n", "\ncravv-connect setup --join cravv-join:", "type `/cravv`"} {
		if !strings.Contains(quick, want) {
			t.Errorf("README lacks %q", want)
		}
	}
	if strings.Contains(quick, "\u2014") {
		t.Error("README has an em dash")
	}
}
