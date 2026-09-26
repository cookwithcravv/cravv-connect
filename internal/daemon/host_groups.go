package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// runGroup is the record of a running run's process group, kept in the
// runs folder while the run lives: a daemon that stopped without ending
// its runs (a crash) kills them when it starts again. Boot names the boot
// of the machine, so a group ID from before a restart is never killed.
type runGroup struct {
	PGID int    `json:"pgid"`
	Boot string `json:"boot"`
}

func (h *SessionHost) groupFile(runID string) string {
	return filepath.Join(h.d.RunDir, "run-"+strings.ToLower(runID)+".group")
}

// startGroup records the process group of session id's run runID.
func (h *SessionHost) startGroup(id, runID string, pgid int) {
	h.mu.Lock()
	h.groups[id] = pgid
	h.mu.Unlock()
	b, _ := json.Marshal(runGroup{PGID: pgid, Boot: bootID()})
	if err := os.WriteFile(h.groupFile(runID), b, 0o600); err != nil {
		h.d.Log.Warn("record run process group", "err", err)
	}
}

// endGroup forgets the run's process group once the run is over (the
// runner has ended it).
func (h *SessionHost) endGroup(id, runID string) {
	h.mu.Lock()
	delete(h.groups, id)
	h.mu.Unlock()
	_ = os.Remove(h.groupFile(runID))
}

// killGroupOf kills the process group of session id's run, if one runs.
func (h *SessionHost) killGroupOf(id string) {
	h.mu.Lock()
	pgid := h.groups[id]
	h.mu.Unlock()
	if pgid != 0 {
		if err := killPGID(pgid); err != nil {
			h.d.Log.Warn("kill run process group", "err", err)
		}
	}
}

// killRecordedGroups kills the process groups that runs of an earlier
// daemon recorded in this boot, and removes the records.
func (h *SessionHost) killRecordedGroups() {
	files, _ := filepath.Glob(filepath.Join(h.d.RunDir, "run-*.group"))
	boot := bootID()
	for _, f := range files {
		var g runGroup
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &g) == nil && boot != "" && g.Boot == boot {
			if err := killPGID(g.PGID); err != nil {
				h.d.Log.Warn("kill a stale run's process group", "err", err)
			}
		}
		_ = os.Remove(f)
	}
}

// maxAncestry bounds InRun's walk up the process tree.
const maxAncestry = 64

// InRun reports whether process pid is inside a live managed run: in a
// run's process group, or a descendant of a process that is (its agent
// leads the group). pid -1 (the peer is unknown) is inside a run whenever
// one is live. The IPC server uses it to let such a process only bind with
// its run token. A process that both left the group and was reparented
// (setsid plus a double fork) is not found: this is defense in depth, not
// a boundary (docs/security.md).
func (h *SessionHost) InRun(pid int) bool {
	h.mu.Lock()
	live := make(map[int]bool, len(h.groups))
	for _, g := range h.groups {
		live[g] = true
	}
	h.mu.Unlock()
	if len(live) == 0 {
		return false
	}
	if pid < 0 {
		return true
	}
	for p, i := pid, 0; p > 1 && i < maxAncestry; i++ {
		if live[p] {
			return true
		}
		ppid, pgid, err := h.d.ProcParent(p)
		if err != nil {
			return false
		}
		if live[pgid] {
			return true
		}
		p = ppid
	}
	return false
}
