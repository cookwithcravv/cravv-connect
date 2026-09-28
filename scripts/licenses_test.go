package scripts

import (
	"os"
	"os/exec"
	"testing"
)

// THIRD_PARTY_LICENSES, shipped in every release archive, must list every
// module linked into the binaries: regenerate it after changing go.mod with
// scripts/third_party_licenses.sh > THIRD_PARTY_LICENSES.
func TestThirdPartyLicensesUpToDate(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the module graph")
	}
	cmd := exec.Command("sh", "scripts/third_party_licenses.sh")
	cmd.Dir = ".."
	want, err := cmd.Output()
	if err != nil {
		t.Fatalf("third_party_licenses.sh: %v", err)
	}
	have, err := os.ReadFile("../THIRD_PARTY_LICENSES")
	if err != nil {
		t.Fatal(err)
	}
	if string(have) != string(want) {
		t.Fatal("THIRD_PARTY_LICENSES is out of date: run scripts/third_party_licenses.sh > THIRD_PARTY_LICENSES")
	}
}
