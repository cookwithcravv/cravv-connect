package ipc

import (
	"fmt"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// v2 Phase 3: managed sessions (offers, runs, open and close).
const (
	MethodSessionRunBind = "session.run_bind"
	MethodOffersList     = "offers.list"
	MethodOffersSet      = "offers.set"
	MethodOffersRemove   = "offers.remove"
	MethodManagedList    = "managed.list"
	MethodManagedOpen    = "managed.open"
	MethodManagedClose   = "managed.close"
)

// RunMethods are the only methods a managed run's connection may call once
// its run token bound it (v2 spec 3.2): its own session's messages, tasks,
// files and links. It can never share, connect, decide, unlock, change
// offers or reach any machine-wide control, and it cannot read the inbox:
// that is the host's run queue, and the run gets its item in the prompt.
var RunMethods = map[string]bool{
	MethodSessionRegister: true,
	MethodSessionRunBind:  true,
	MethodChatSend:        true,
	MethodTaskGet:         true,
	MethodTaskClaim:       true,
	MethodTaskUpdate:      true,
	MethodTaskComplete:    true,
	MethodTaskFail:        true,
	MethodFileSend:        true,
	MethodLinks:           true,
}

// ErrRunRefused is returned for any other method on a run's connection.
var ErrRunRefused = fmt.Errorf("%w: a managed run may only use its own session's messages, tasks, files and link", core.ErrNotPermitted)

// ErrRunUnbound is returned to a process inside a managed run that calls
// anything but session.register and session.run_bind before binding.
var ErrRunUnbound = fmt.Errorf("%w: a process inside a managed run may only bind with its run token", core.ErrNotPermitted)

// RunBindParams binds this connection to the managed session of the run
// holding the token (the child's cravv-connect mcp reads it from
// CRAVV_RUN_TOKEN).
type RunBindParams struct {
	RunToken string `json:"run_token"`
}

// RemoteOfferView is a managed-session offer a paired machine makes to
// this one: connect to <machine>/new:<label>. Label is validated.
type RemoteOfferView struct {
	Label         string `json:"label"`
	Agent         string `json:"agent,omitempty"`
	MaxPermission string `json:"max_permission"`
}

// OfferView is one offer rule as the owner sees it.
type OfferView struct {
	Machine        string    `json:"machine"`
	Label          string    `json:"label"`
	Folder         string    `json:"folder"`
	Agent          string    `json:"agent"`
	Permission     string    `json:"permission"`
	RunMode        string    `json:"run_mode"`
	MaxConcurrent  int       `json:"max_concurrent"`
	IdleTimeoutS   int       `json:"idle_timeout_s"`
	MaxTurnsPerRun int       `json:"max_turns_per_run"`
	RunTimeoutS    int       `json:"run_timeout_s"`
	RunsPerHour    int       `json:"runs_per_hour"`
	RunsPerDay     int       `json:"runs_per_day"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// OffersListParams lists the offers to Machine ("" for every machine).
type OffersListParams struct {
	Machine string `json:"machine,omitempty"`
}

type OffersListResult struct {
	Offers []OfferView `json:"offers"`
}

// OfferSetParams creates or replaces the offer Label to Machine. Zero
// numbers take the defaults; ShellConfirm must be "shell" for run mode shell.
type OfferSetParams struct {
	Machine        string `json:"machine"`
	Label          string `json:"label"`
	Folder         string `json:"folder"`
	Agent          string `json:"agent,omitempty"`
	Permission     string `json:"permission"`
	RunMode        string `json:"run_mode,omitempty"`
	ShellConfirm   string `json:"shell_confirm,omitempty"`
	MaxConcurrent  int    `json:"max_concurrent,omitempty"`
	IdleTimeoutS   int    `json:"idle_timeout_s,omitempty"`
	MaxTurnsPerRun int    `json:"max_turns_per_run,omitempty"`
	RunTimeoutS    int    `json:"run_timeout_s,omitempty"`
	RunsPerHour    int    `json:"runs_per_hour,omitempty"`
	RunsPerDay     int    `json:"runs_per_day,omitempty"`
}

type OfferRemoveParams struct {
	Machine string `json:"machine"`
	Label   string `json:"label"`
}

// ManagedView is one open managed session. State is idle, running or live
// (a human has it open).
type ManagedView struct {
	Name       string    `json:"name"`
	Machine    string    `json:"machine"`
	Offer      string    `json:"offer,omitempty"`
	Folder     string    `json:"folder"`
	RunMode    string    `json:"run_mode,omitempty"`
	Link       int64     `json:"link,omitempty"`
	State      string    `json:"state"`
	Started    bool      `json:"started"`
	LastActive time.Time `json:"last_active"`
}

type ManagedListResult struct {
	Sessions []ManagedView `json:"sessions"`
}

type ManagedNameParams struct {
	Name string `json:"name"`
}

// ManagedOpenResult is how to open the session's conversation: run Command
// in Folder. The session's queue waits until this connection ends.
type ManagedOpenResult struct {
	Name    string   `json:"name"`
	Folder  string   `json:"folder"`
	Command []string `json:"command"`
}
