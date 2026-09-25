package auth

import (
	"errors"
	"runtime"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
)

func TestFakeVerifier(t *testing.T) {
	f := Fake{Password: "hunter2"}
	cases := []struct {
		pw   string
		want error
	}{
		{"hunter2", nil},
		{"hunter3", core.ErrBadPassword},
		{"", core.ErrBadPassword},
		{"hunter2 ", core.ErrBadPassword},
	}
	for _, c := range cases {
		if err := f.Verify("me", c.pw); !errors.Is(err, c.want) {
			t.Errorf("Verify(%q) = %v, want %v", c.pw, err, c.want)
		}
	}
}

func TestUnavailableVerifierAlwaysFails(t *testing.T) {
	err := unavailable{}.Verify("me", "anything")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if err.Error() != "password auth unavailable (built without cgo)" {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestDefaultPAMService(t *testing.T) {
	want := "login"
	if runtime.GOOS == "darwin" {
		want = "chkpasswd"
	}
	if got := DefaultPAMService(); got != want {
		t.Fatalf("DefaultPAMService() = %q, want %q", got, want)
	}
	if DefaultVerifier() == nil {
		t.Fatal("DefaultVerifier() returned nil")
	}
}

func TestCurrentUsername(t *testing.T) {
	name, err := CurrentUsername()
	if err != nil {
		t.Fatal(err)
	}
	if name == "" {
		t.Fatal("empty username")
	}
}
