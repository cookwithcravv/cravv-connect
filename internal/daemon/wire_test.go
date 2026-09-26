package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/auth"
	"github.com/cravv/cravv-connect/internal/core"
)

// The password lockout lives in the store, so restarting the daemon (or
// killing it to reset the counter) does not lift it.
func TestGuardLockoutSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	d := d2NewDaemon(t, dir, &d2Relay{})
	for i := 0; i < core.LockoutFailures; i++ {
		if err := d.Guard().Check("wrong"); !errors.Is(err, core.ErrBadPassword) {
			t.Fatalf("attempt %d err = %v, want ErrBadPassword", i+1, err)
		}
	}
	if err := d.Guard().Check("pw"); !errors.Is(err, core.ErrLocked) {
		t.Fatalf("after %d failures err = %v, want ErrLocked", core.LockoutFailures, err)
	}
	d.Close()

	d2 := d2NewDaemon(t, dir, &d2Relay{})
	defer d2.Close()
	if err := d2.Guard().Check("pw"); !errors.Is(err, core.ErrLocked) {
		t.Fatalf("after restart err = %v, want ErrLocked", err)
	}
}

// d2AcceptAll is a verifier that accepts every password, like a PAM stack
// ending in pam_permit.
type d2AcceptAll struct{}

func (d2AcceptAll) Verify(string, string) error { return nil }

// A verifier that accepts any password would make every password gate a
// no-op, so the daemon refuses to start with one.
func TestNewRefusesVerifierThatAcceptsAnyPassword(t *testing.T) {
	dir := t.TempDir()
	opts := d2Options(dir, &d2Relay{})
	opts.Verifier = d2AcceptAll{}
	d, err := New(opts)
	if err == nil {
		d.Close()
		t.Fatal("New accepted a verifier that accepts any password")
	}
	if !errors.Is(err, auth.ErrAcceptsAnyPassword) {
		t.Fatalf("New err = %v, want ErrAcceptsAnyPassword", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "store.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("store opened before the verifier self-test: %v", err)
	}
}

// d2CountingVerifier accepts "pw" and counts every Verify call.
type d2CountingVerifier struct{ calls atomic.Int32 }

func (v *d2CountingVerifier) Verify(user, password string) error {
	v.calls.Add(1)
	return auth.Fake{Password: "pw"}.Verify(user, password)
}

// On Linux with pam_faillock every self-test is a failed login against the OS
// account, so it runs once per PAM service, not on every daemon start.
func TestSelfTestRunsOncePerPAMService(t *testing.T) {
	dir := t.TempDir()
	v := &d2CountingVerifier{}
	open := func(service string) {
		t.Helper()
		opts := d2Options(dir, &d2Relay{})
		opts.Verifier = v
		opts.Config.PAMService = service
		d, err := New(opts)
		if err != nil {
			t.Fatalf("New(%q): %v", service, err)
		}
		d.Close()
	}
	open("login")
	if n := v.calls.Load(); n != 1 {
		t.Fatalf("first start: %d verifier calls, want 1", n)
	}
	open("login")
	if n := v.calls.Load(); n != 1 {
		t.Fatalf("second start re-ran the self-test (%d calls)", n)
	}
	open("sshd")
	if n := v.calls.Load(); n != 2 {
		t.Fatalf("changed PAM service: %d calls, want 2", n)
	}
	open("sshd")
	if n := v.calls.Load(); n != 2 {
		t.Fatalf("restart after service change: %d calls, want 2", n)
	}
}

// A store that exists but was never self-tested (or for another service) is
// still checked, and the daemon refuses to start.
func TestSelfTestStillRefusesOnExistingStore(t *testing.T) {
	dir := t.TempDir()
	d := d2NewDaemon(t, dir, &d2Relay{})
	d.Close()
	opts := d2Options(dir, &d2Relay{})
	opts.Verifier = d2AcceptAll{}
	opts.Config.PAMService = "other"
	if d, err := New(opts); !errors.Is(err, auth.ErrAcceptsAnyPassword) {
		if d != nil {
			d.Close()
		}
		t.Fatalf("New err = %v, want ErrAcceptsAnyPassword", err)
	}
}

type fakePAMFile struct {
	size  int64
	mtime time.Time
}

func (f fakePAMFile) Name() string       { return "login" }
func (f fakePAMFile) Size() int64        { return f.size }
func (f fakePAMFile) Mode() os.FileMode  { return 0o644 }
func (f fakePAMFile) ModTime() time.Time { return f.mtime }
func (f fakePAMFile) IsDir() bool        { return false }
func (f fakePAMFile) Sys() any           { return nil }

// Editing the PAM configuration (say, adding pam_permit) must not ride on an
// earlier pass: the cached result is keyed by the file's size and mtime too.
func TestSelfTestRerunsWhenPAMConfigChanges(t *testing.T) {
	dir := t.TempDir()
	v := &d2CountingVerifier{}
	file := fakePAMFile{size: 100, mtime: time.Unix(1000, 0)}
	var statErr error
	var statted []string
	open := func() {
		t.Helper()
		opts := d2Options(dir, &d2Relay{})
		opts.Verifier = v
		opts.Config.PAMService = "login"
		opts.StatPAMConfig = func(path string) (os.FileInfo, error) {
			statted = append(statted, path)
			if statErr != nil {
				return nil, statErr
			}
			return file, nil
		}
		d, err := New(opts)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		d.Close()
	}
	want := func(n int32, what string) {
		t.Helper()
		if got := v.calls.Load(); got != n {
			t.Fatalf("%s: %d self-tests, want %d", what, got, n)
		}
	}
	open()
	want(1, "first start")
	if len(statted) == 0 || statted[0] != "/etc/pam.d/login" {
		t.Fatalf("stat paths = %v", statted)
	}
	open()
	want(1, "unchanged file")
	file.mtime = time.Unix(2000, 0)
	open()
	want(2, "changed mtime")
	file.size = 101
	open()
	want(3, "changed size")
	statErr = os.ErrPermission
	open()
	want(4, "unreadable file")
	open()
	want(4, "still unreadable")
}

// d2BrokenVerifier fails every check with a PAM-style service error.
type d2BrokenVerifier struct{ calls atomic.Int32 }

func (v *d2BrokenVerifier) Verify(string, string) error {
	v.calls.Add(1)
	return errors.New("pam authenticate: Module is unknown")
}

// A verifier that errors instead of rejecting a wrong password cannot check
// passwords: the daemon still starts (so status can say so), status reports
// it, and the result is not cached.
func TestBrokenVerifierStartsWithStatusWarning(t *testing.T) {
	dir := t.TempDir()
	v := &d2BrokenVerifier{}
	for i := range 2 {
		opts := d2Options(dir, &d2Relay{})
		opts.Verifier = v
		d, err := New(opts)
		if err != nil {
			t.Fatalf("start %d: New refused: %v", i+1, err)
		}
		st, err := d.Status().Status(context.Background())
		d.Close()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range st.Errors {
			if strings.HasPrefix(e, "password check is not working: ") && strings.Contains(e, "Module is unknown") {
				found = true
			}
		}
		if !found {
			t.Fatalf("start %d: status errors %q", i+1, st.Errors)
		}
	}
	if n := v.calls.Load(); n != 2 {
		t.Fatalf("self-test ran %d times over 2 starts, want 2 (not cached)", n)
	}
}
