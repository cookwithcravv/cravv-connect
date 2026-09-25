//go:build !unix

package pathguard

import "os"

// Open is only supported on unix, where the file can be opened without
// following symlinks and checked against what Check approved.
func (o *Outbound) Open(projectDir, path string) (*os.File, os.FileInfo, error) {
	return nil, nil, refuse("opening files for sending is not supported on this OS")
}
