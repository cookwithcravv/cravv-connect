package daemon

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// Confirmation codes (v2 spec 7.2): for clients that cannot show an
// elicitation form, the daemon shows a 4-digit single-use code in a desktop
// notification. The human types it in the chat and the agent passes it on;
// the model never sees the code, only the human does.
const (
	CodeValidity  = 10 * time.Minute
	CodeMaxWrong  = 3
	codeDigits    = 4
	codeSpace     = 10000 // 10^codeDigits
	codeTitleText = "cravv-connect code "
)

// Errors of the confirmation-code check (the API maps them to bad_request
// and busy).
var (
	ErrBadCode    = errors.New("wrong or expired confirmation code: ask the human to read the code in the newest cravv-connect notification")
	ErrCodeLocked = errors.New("too many wrong confirmation codes for this item: wait until the code expires, or use cravv-connect approvals or cravv-connect links in a terminal")
	ErrNoDesktop  = errors.New("this machine cannot show desktop notifications")
)

// DesktopAvailable reports whether n can show a notification to the human.
// A notifier says so by implementing Available; any other non-nil notifier
// is assumed to work.
func DesktopAvailable(n DesktopNotifier) bool {
	if n == nil {
		return false
	}
	if a, ok := n.(interface{ Available() bool }); ok {
		return a.Available()
	}
	return true
}

type codeState struct {
	code    string
	expires time.Time
	wrong   int
}

// ConfirmCodes issues and checks codes, one live code per pending item.
type ConfirmCodes struct {
	clock   core.Clock
	desktop DesktopNotifier
	rand    func() (int, error)

	mu    sync.Mutex
	codes map[string]*codeState // item ID -> code
}

// NewConfirmCodes shows codes on desktop.
func NewConfirmCodes(clock core.Clock, desktop DesktopNotifier) *ConfirmCodes {
	return &ConfirmCodes{clock: clock, desktop: desktop, rand: randomCode, codes: map[string]*codeState{}}
}

func randomCode() (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(codeSpace))
	if err != nil {
		return 0, err
	}
	return int(n.Int64()), nil
}

// Show displays the item's code with text (which says what is being
// decided). An unexpired code is shown again rather than replaced, so a
// code the human already read stays valid. It fails with ErrNoDesktop
// when nothing can be shown and ErrCodeLocked after CodeMaxWrong wrong
// tries until that code expires. The code is never returned.
func (c *ConfirmCodes) Show(item, text string) error {
	if !DesktopAvailable(c.desktop) {
		return ErrNoDesktop
	}
	c.mu.Lock()
	now := c.clock.Now()
	st, ok := c.codes[item]
	if ok && !now.Before(st.expires) {
		ok = false
	}
	if ok && st.wrong >= CodeMaxWrong {
		c.mu.Unlock()
		return ErrCodeLocked
	}
	if !ok {
		n, err := c.rand()
		if err != nil {
			c.mu.Unlock()
			return err
		}
		st = &codeState{code: fmt.Sprintf("%0*d", codeDigits, n), expires: now.Add(CodeValidity)}
		c.codes[item] = st
	}
	code := st.code
	c.mu.Unlock()
	c.desktop.Notify(codeTitleText+code, text+" Code "+code)
	return nil
}

// Check verifies code for item. A right code is used up; a wrong one
// counts toward CodeMaxWrong, after which the item's code is dead until it
// expires.
func (c *ConfirmCodes) Check(item, code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.codes[item]
	if !ok || !c.clock.Now().Before(st.expires) {
		return ErrBadCode
	}
	if st.wrong >= CodeMaxWrong {
		return ErrCodeLocked
	}
	if subtle.ConstantTimeCompare([]byte(st.code), []byte(code)) != 1 {
		st.wrong++
		return ErrBadCode
	}
	delete(c.codes, item)
	return nil
}

// Forget drops an item's code (the item was decided some other way).
func (c *ConfirmCodes) Forget(item string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.codes, item)
}
