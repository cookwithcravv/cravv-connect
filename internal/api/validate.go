package api

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

var aliasRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)

func badRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ipc.ErrBadRequest, fmt.Sprintf(format, args...))
}

func required(name, v string) error {
	if strings.TrimSpace(v) == "" {
		return badRequest("%s is required", name)
	}
	return nil
}

func validAlias(a string) error {
	if !aliasRE.MatchString(a) {
		return badRequest("alias must be 1 to 24 characters of a-z, 0-9 and '-', starting with a letter or digit")
	}
	return nil
}

func absPath(name, p string) error {
	if !filepath.IsAbs(p) {
		return badRequest("%s must be an absolute path", name)
	}
	return nil
}
