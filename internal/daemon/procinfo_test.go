//go:build darwin || linux

package daemon

import (
	"os"
	"syscall"
	"testing"
)

func TestProcParentAndBootID(t *testing.T) {
	ppid, pgid, err := procParent(os.Getpid())
	if err != nil || ppid != os.Getppid() || pgid != syscall.Getpgrp() {
		t.Fatalf("procParent(self) = %d, %d, %v; want %d, %d", ppid, pgid, err, os.Getppid(), syscall.Getpgrp())
	}
	if _, _, err := procParent(1 << 30); err == nil {
		t.Fatal("a missing process must be an error")
	}
	if id := bootID(); id == "" || id != bootID() {
		t.Fatalf("bootID %q", id)
	}
}
