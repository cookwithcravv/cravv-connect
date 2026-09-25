//go:build !(darwin || linux)

package daemon

import "errors"

// diskFree is unsupported here; FileService skips the disk check on error.
func diskFree(dir string) (uint64, error) {
	return 0, errors.ErrUnsupported
}
