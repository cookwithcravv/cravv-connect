package cli

import (
	"strings"

	"github.com/cookwithcravv/cravv-connect/internal/app"
	"github.com/cookwithcravv/cravv-connect/internal/auth"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// friendlyErrors replaces daemon error text with advice for the human, keyed
// by wire kind. match narrows a kind that several daemon errors share to the
// one the advice is about; nil matches every error of the kind.
var friendlyErrors = []struct {
	kind  string
	match func(err error) bool
	msg   string
}{
	{
		kind:  app.KindAuthUnavailable,
		match: func(err error) bool { return strings.Contains(err.Error(), auth.ErrUnavailable.Error()) },
		msg:   "password check unavailable: this cravv-connect binary was built without PAM support; install a release build or rebuild with CGO_ENABLED=1",
	},
}

// userMessage returns the text shown for err on stderr.
func userMessage(err error) string {
	for _, f := range friendlyErrors {
		if ipc.IsKind(err, f.kind) && (f.match == nil || f.match(err)) {
			return f.msg
		}
	}
	return err.Error()
}
