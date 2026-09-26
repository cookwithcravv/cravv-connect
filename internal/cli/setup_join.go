package cli

import (
	"errors"
	"fmt"

	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/joincode"
	"github.com/cravv/cravv-connect/internal/relayaddr"
)

// setupJoinCode parses the code given to `setup --join` and applies the relay
// rule: https, or plain http only for this machine or a private network.
func setupJoinCode(s string) (joincode.Code, error) {
	if !joincode.Is(s) {
		return joincode.Code{}, errors.New("setup --join needs a join code (cravv-join:...): run `cravv-connect pair` on the other machine to show one")
	}
	c, err := joincode.Parse(s)
	if err != nil {
		return joincode.Code{}, err
	}
	if err := relayaddr.Check(c.Relay); err != nil {
		return joincode.Code{}, fmt.Errorf("refusing the relay in this join code (%s): %w", c.Relay, err)
	}
	return c, nil
}

// joinFlow sets this machine up on the relay of a join code and pairs it with
// the machine that showed the code: init, the daemon, join (password), the
// alias, then the agent integrations. A machine set up for another relay is
// refused unless --reset.
func (s *setup) joinFlow(c joincode.Code, cfg config.Config, configured bool) error {
	if configured {
		have, err := relayOrigin(cfg.RelayURL)
		if err != nil {
			have = cfg.RelayURL
		}
		if have != c.Relay {
			return fmt.Errorf("this machine is set up for relay %s, but the join code is for %s; to move it, run `cravv-connect setup --reset --join <code>` (you pair again with every peer)", have, c.Relay)
		}
		fmt.Fprintf(s.w, "This machine is set up for relay %s as %s.\n", cfg.RelayURL, cfg.DeviceName)
	} else {
		s.section("Relay")
		ok, err := s.agree(fmt.Sprintf("Join relay %s?", c.Relay), false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(s.w, "Nothing changed.")
			return nil
		}
		if err := s.sys.RelayHealthy(s.ctx, c.Relay); err != nil {
			return fmt.Errorf("the relay %s does not answer: %w", c.Relay, err)
		}
		s.section("This machine")
		if err := runInit(s.ctx, s.env, c.Relay, "", s.o.name, s.o.reset); err != nil {
			return err
		}
	}
	// No mailbox yet: the invite arrives while joining.
	if _, err := s.daemonOnline(!configured, false); err != nil {
		return err
	}
	s.section("Join")
	if err := withConn(s.ctx, s.env, func(cl Caller) error { return joinNow(s.ctx, s.env, cl, c.Bind.String()) }); err != nil {
		return err
	}
	st, err := s.status()
	if err != nil {
		return err
	}
	if _, err := s.waitRelay(st, ""); err != nil {
		return err
	}
	if err := s.agents(); err != nil {
		return err
	}
	s.done()
	return nil
}
