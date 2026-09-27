//go:build unix

package pathguard

import (
	"errors"
	"os"
	"syscall"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

// afterCheckHook, when set by tests, runs between Check and the open so a
// test can swap the file in that window.
var afterCheckHook func(real string)

// Open runs Check and then opens the file without following a final
// symlink (O_NOFOLLOW) and without blocking on a FIFO (O_NONBLOCK). The
// opened file must be the very file Check approved (os.SameFile on the
// fstat result), a regular file with a single link (a hard link could
// expose a file stored outside the roots) and no larger than
// core.MaxFileBytes. The returned FileInfo comes from fstat of the open
// descriptor; callers must read at most info.Size() bytes. The caller owns
// the file and must close it. Every refusal wraps core.ErrPathRefused.
func (o *Outbound) Open(projectDir, path string) (*os.File, os.FileInfo, error) {
	real, checked, err := o.Check(projectDir, path)
	if err != nil {
		return nil, nil, err
	}
	if afterCheckHook != nil {
		afterCheckHook(real)
	}
	fd, err := openNoFollow(real)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, nil, refuse("%s changed into a symlink", path)
		}
		return nil, nil, refuse("%s cannot be opened", path)
	}
	f := os.NewFile(uintptr(fd), real)
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, refuse("%s cannot be read", path)
	}
	if err := verifyOpened(path, checked, fi); err != nil {
		f.Close()
		return nil, nil, err
	}
	// Reads should block normally now that it is known to be a regular file.
	if err := syscall.SetNonblock(fd, false); err != nil {
		f.Close()
		return nil, nil, refuse("%s cannot be read", path)
	}
	return f, fi, nil
}

func openNoFollow(real string) (int, error) {
	for {
		fd, err := syscall.Open(real, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if err == syscall.EINTR {
			continue
		}
		return fd, err
	}
}

// verifyOpened checks the fstat result of the opened descriptor against
// the FileInfo Check returned.
func verifyOpened(path string, checked, fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return refuse("%s is not a regular file", path)
	}
	if !os.SameFile(checked, fi) {
		return refuse("%s changed while it was being opened", path)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return refuse("%s cannot be inspected", path)
	}
	if uint64(st.Nlink) > 1 {
		return refuse("%s has more than one hard link", path)
	}
	if fi.Size() > core.MaxFileBytes {
		return refuse("%s is larger than 100 MB", path)
	}
	return nil
}
