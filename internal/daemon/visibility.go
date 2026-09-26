package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/cravv/cravv-connect/internal/core"
)

// ParseVisibility reads "private", "all-peers" or "peers:<alias>[,<alias>...]"
// ("" means private). Aliases (or machine IDs) must name paired machines.
func ParseVisibility(ctx context.Context, s string, peers PeerResolver) (core.Visibility, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "", string(core.VisibilityPrivate):
		return core.Visibility{Mode: core.VisibilityPrivate}, nil
	case string(core.VisibilityAllPeers):
		return core.Visibility{Mode: core.VisibilityAllPeers}, nil
	}
	list, ok := strings.CutPrefix(s, string(core.VisibilityPeers)+":")
	if !ok {
		return core.Visibility{}, ErrBadVisibility
	}
	v := core.Visibility{Mode: core.VisibilityPeers}
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		p, _, err := peers.Resolve(ctx, name)
		if err != nil {
			return core.Visibility{}, fmt.Errorf("visibility: %w", err)
		}
		if !slices.Contains(v.Peers, p.MachineID) {
			v.Peers = append(v.Peers, p.MachineID)
		}
	}
	if !v.Valid() {
		return core.Visibility{}, ErrBadVisibility
	}
	return v, nil
}

// FormatVisibility writes v the way ParseVisibility reads it, naming
// machines by their local alias (a machine no longer paired shows its short ID).
func FormatVisibility(ctx context.Context, v core.Visibility, peers PeerResolver) string {
	if v.Mode != core.VisibilityPeers {
		return string(v.Mode)
	}
	names := make([]string, 0, len(v.Peers))
	for _, id := range v.Peers {
		if p, _, err := peers.Resolve(ctx, string(id)); err == nil {
			names = append(names, p.Alias)
		} else {
			names = append(names, id.Short())
		}
	}
	return string(core.VisibilityPeers) + ":" + strings.Join(names, ",")
}
