//go:build !(darwin || linux)

package daemon

import "errors"

func procParent(int) (int, int, error) { return 0, 0, errors.New("not supported on this OS") }

func bootID() string { return "" }
