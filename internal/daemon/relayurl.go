package daemon

import "github.com/cookwithcravv/cravv-connect/internal/relayaddr"

// validRelayURL accepts a relay URL learned from a peer (pairing payload or
// control.relay_moved). The rule is relayaddr.Check, shared with
// `cravv-connect setup --join`: https, or plain http only for this machine or
// a private network.
func validRelayURL(raw string) error { return relayaddr.Check(raw) }
