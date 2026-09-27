package auth

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/core"
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

func TestPAMServiceAllowlist(t *testing.T) {
	var allowed []string
	switch runtime.GOOS {
	case "darwin":
		allowed = []string{"chkpasswd", "checkpw"}
	case "linux":
		allowed = []string{"login", "system-auth"}
	}
	for _, s := range allowed {
		if !PAMServiceAllowed(s) {
			t.Errorf("PAMServiceAllowed(%q) = false on %s", s, runtime.GOOS)
		}
		if err := CheckPAMService(s); err != nil {
			t.Errorf("CheckPAMService(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"", "passwd", "sudo", "su", "sshd", "other", "Login", " login", "../login", "login\x00"} {
		if PAMServiceAllowed(s) {
			t.Errorf("PAMServiceAllowed(%q) = true", s)
		}
		err := CheckPAMService(s)
		if !errors.Is(err, ErrServiceNotAllowed) || !errors.Is(err, ErrUnavailable) {
			t.Errorf("CheckPAMService(%q) = %v, want ErrServiceNotAllowed wrapping ErrUnavailable", s, err)
		}
	}
	if len(allowed) > 0 && !PAMServiceAllowed(DefaultPAMService()) {
		t.Fatalf("default service %q is not allowlisted", DefaultPAMService())
	}
	if got := AllowedPAMServices(); len(got) != len(allowed) {
		t.Fatalf("AllowedPAMServices() = %v, want %v", got, allowed)
	}
}

// NewPAM with a service that is not allowlisted never reaches PAM: the
// verifier fails every check with a clear error (never ErrBadPassword, so
// it cannot trip the lockout either).
func TestNewPAMRefusesServiceNotAllowed(t *testing.T) {
	for _, s := range []string{"sudo", "passwd", "other"} {
		err := NewPAM(s).Verify("me", "pw")
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("NewPAM(%q).Verify = %v, want ErrUnavailable", s, err)
		}
		if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
			if !errors.Is(err, ErrServiceNotAllowed) || !strings.Contains(err.Error(), "pam service not allowed") {
				t.Fatalf("NewPAM(%q).Verify = %v, want a \"pam service not allowed\" error", s, err)
			}
		}
		if errors.Is(err, core.ErrBadPassword) {
			t.Fatalf("NewPAM(%q) error counts as a bad password", s)
		}
	}
}
