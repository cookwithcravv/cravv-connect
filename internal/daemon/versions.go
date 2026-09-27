package daemon

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// VersionNotices tracks protocol mismatches for status: peers that still
// send v1 traffic (they were told control.unsupported), and peers that told
// this machine it is too old.
type VersionNotices struct {
	mu       sync.Mutex
	outdated map[core.MachineID]string // alias
	newer    map[core.MachineID]string // "alias needs protocol N"
}

// NewVersionNotices returns an empty tracker.
func NewVersionNotices() *VersionNotices {
	return &VersionNotices{outdated: map[core.MachineID]string{}, newer: map[core.MachineID]string{}}
}

// Replier decorates a gate replier: every control.unsupported sent also
// records the peer as outdated.
func (v *VersionNotices) Replier(inner GateReplier) GateReplier {
	return noticingReplier{GateReplier: inner, v: v}
}

type noticingReplier struct {
	GateReplier
	v *VersionNotices
}

func (r noticingReplier) Unsupported(ctx context.Context, peer store.Peer) {
	r.v.mu.Lock()
	r.v.outdated[peer.MachineID] = peer.Alias
	r.v.mu.Unlock()
	r.GateReplier.Unsupported(ctx, peer)
}

// Seen clears the outdated notice for a peer that sent valid link traffic:
// it has upgraded.
func (v *VersionNotices) Seen(peer store.Peer) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.outdated, peer.MachineID)
}

// HandleUnsupported handles control.unsupported from a peer.
func (v *VersionNotices) HandleUnsupported(_ context.Context, peer store.Peer, env core.Envelope) error {
	b, err := decodeEnvBody[core.UnsupportedBody](env.Body)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.newer[peer.MachineID] = fmt.Sprintf("%s needs cravv-connect protocol %d; this machine speaks %d: upgrade cravv-connect here", peer.Alias, b.MinVersion, core.ProtocolVersion)
	return nil
}

// Errors reports the mismatches, for status.
func (v *VersionNotices) Errors() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for _, alias := range v.outdated {
		out = append(out, fmt.Sprintf("%s runs an older cravv-connect without session links: it needs an upgrade", alias))
	}
	for _, msg := range v.newer {
		out = append(out, msg)
	}
	sort.Strings(out)
	return out
}
