package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// Confirmation codes (v2 spec 7.2): for clients that cannot show an
// elicitation form, the daemon shows a 4-digit single-use code in a desktop
// notification. The human types it in the chat and the agent passes it on;
// the model never sees the code, only the human does.
//
// Guessing is bounded twice: an item takes CodeMaxWrong wrong codes over
// its whole life (a fresh code after expiry does not reset the count), and
// then never gets a code again, so the human decides it with their
// password; and a session takes CodeSessionMaxWrong wrong codes in
// CodeSessionWindow over all its items. With WithCodeState the counts
// survive a daemon restart.
const (
	CodeValidity        = 10 * time.Minute
	CodeMaxWrong        = 3
	CodeSessionMaxWrong = 10
	CodeSessionWindow   = 24 * time.Hour
	codeLockRetention   = 30 * 24 * time.Hour // how long an item's wrong count is kept after its last wrong try
	codeDigits          = 4
	codeSpace           = 10000 // 10^codeDigits
	codeTitleText       = "cravv-connect code "
	settingCodeWrong    = "confirm_code_wrong" // JSON codeLedger
)

// Errors of the confirmation-code check (the API maps them to bad_request
// and busy).
var (
	ErrBadCode    = errors.New("wrong or expired confirmation code: ask the human to read the code in the newest cravv-connect notification")
	ErrCodeLocked = errors.New("too many wrong confirmation codes for this item or this chat: the human decides with their password, with cravv-connect approvals or cravv-connect links in a terminal, or in the web UI (cravv-connect ui)")
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
}

// codeLedger is the wrong tries, as persisted.
type codeLedger struct {
	Items    map[string]itemWrong `json:"items,omitempty"`    // item ID -> wrong tries over its life
	Sessions map[string][]int64   `json:"sessions,omitempty"` // session ID -> times (unix ms) of wrong tries
}

type itemWrong struct {
	N    int   `json:"n"`
	Last int64 `json:"last"` // unix ms of the newest wrong try
}

// CodeStateStore persists the wrong tries (store.SettingsStore satisfies it).
type CodeStateStore interface {
	GetSetting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
}

// ConfirmCodes issues and checks codes, one live code per pending item.
type ConfirmCodes struct {
	clock   core.Clock
	desktop DesktopNotifier
	rand    func() (int, error)
	state   CodeStateStore // nil = in memory only

	mu          sync.Mutex
	codes       map[string]*codeState // item ID -> live code
	wrong       codeLedger
	lockedUntil time.Time // fail closed: the persisted state could not be read
}

// ConfirmCodesOption configures NewConfirmCodes.
type ConfirmCodesOption func(*ConfirmCodes)

// WithCodeState loads the wrong tries from s and writes them back on every
// change. If they cannot be read or parsed, every code is refused for
// CodeSessionWindow (the password path still works).
func WithCodeState(s CodeStateStore) ConfirmCodesOption {
	return func(c *ConfirmCodes) { c.state = s }
}

// NewConfirmCodes shows codes on desktop.
func NewConfirmCodes(clock core.Clock, desktop DesktopNotifier, opts ...ConfirmCodesOption) *ConfirmCodes {
	c := &ConfirmCodes{clock: clock, desktop: desktop, rand: randomCode, codes: map[string]*codeState{}}
	for _, o := range opts {
		o(c)
	}
	c.wrong = codeLedger{Items: map[string]itemWrong{}, Sessions: map[string][]int64{}}
	if c.state != nil {
		if err := c.load(); err != nil {
			c.lockedUntil = clock.Now().Add(CodeSessionWindow)
		}
	}
	return c
}

func (c *ConfirmCodes) load() error {
	v, ok, err := c.state.GetSetting(context.Background(), settingCodeWrong)
	if err != nil || !ok {
		return err
	}
	var l codeLedger
	if err := json.Unmarshal([]byte(v), &l); err != nil {
		return err
	}
	maps.Copy(c.wrong.Items, l.Items)
	maps.Copy(c.wrong.Sessions, l.Sessions)
	return nil
}

// saveLocked persists the wrong tries. A failed write keeps them in
// memory. c.mu must be held.
func (c *ConfirmCodes) saveLocked() {
	if c.state == nil {
		return
	}
	if b, err := json.Marshal(c.wrong); err == nil {
		_ = c.state.SetSetting(context.Background(), settingCodeWrong, string(b))
	}
}

func randomCode() (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(codeSpace))
	if err != nil {
		return 0, err
	}
	return int(n.Int64()), nil
}

// pruneLocked drops expired codes, wrong tries older than the session
// window and item counts past their retention. c.mu must be held.
func (c *ConfirmCodes) pruneLocked(now time.Time) {
	for item, st := range c.codes {
		if !now.Before(st.expires) {
			delete(c.codes, item)
		}
	}
	changed := false
	since := now.Add(-CodeSessionWindow).UnixMilli()
	for s, times := range c.wrong.Sessions {
		kept := slices.DeleteFunc(slices.Clone(times), func(ms int64) bool { return ms <= since })
		switch {
		case len(kept) == 0:
			delete(c.wrong.Sessions, s)
		case len(kept) != len(times):
			c.wrong.Sessions[s] = kept
		default:
			continue
		}
		changed = true
	}
	old := now.Add(-codeLockRetention).UnixMilli()
	for item, w := range c.wrong.Items {
		if w.Last <= old {
			delete(c.wrong.Items, item)
			changed = true
		}
	}
	if changed {
		c.saveLocked()
	}
}

// lockedLocked reports whether session may not use a code for item.
// c.mu must be held.
func (c *ConfirmCodes) lockedLocked(now time.Time, session, item string) bool {
	return now.Before(c.lockedUntil) ||
		c.wrong.Items[item].N >= CodeMaxWrong ||
		len(c.wrong.Sessions[session]) >= CodeSessionMaxWrong
}

// Show displays the item's code with text (which says what is being
// decided) for session's human. An unexpired code is shown again rather
// than replaced, so a code the human already read stays valid. It fails
// with ErrNoDesktop when nothing can be shown and ErrCodeLocked once the
// item or the session had too many wrong tries. The code is never
// returned.
func (c *ConfirmCodes) Show(session, item, text string) error {
	if !DesktopAvailable(c.desktop) {
		return ErrNoDesktop
	}
	c.mu.Lock()
	now := c.clock.Now()
	c.pruneLocked(now)
	if c.lockedLocked(now, session, item) {
		c.mu.Unlock()
		return ErrCodeLocked
	}
	st, ok := c.codes[item]
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

// Check verifies code for item, typed in session's chat. A right code is
// used up; a wrong one counts toward CodeMaxWrong for the item and
// CodeSessionMaxWrong for the session.
func (c *ConfirmCodes) Check(session, item, code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	c.pruneLocked(now)
	if c.lockedLocked(now, session, item) {
		return ErrCodeLocked
	}
	st, ok := c.codes[item]
	if !ok {
		return ErrBadCode
	}
	if subtle.ConstantTimeCompare([]byte(st.code), []byte(code)) != 1 {
		w := c.wrong.Items[item]
		w.N++
		w.Last = now.UnixMilli()
		c.wrong.Items[item] = w
		c.wrong.Sessions[session] = append(c.wrong.Sessions[session], now.UnixMilli())
		if w.N >= CodeMaxWrong {
			delete(c.codes, item)
		}
		c.saveLocked()
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
