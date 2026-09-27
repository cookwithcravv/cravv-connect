package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/cookwithcravv/cravv-connect/internal/config"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/spf13/cobra"
)

// SettingRelayAdminToken is the settings key init stores the relay admin
// token under. The daemon reads it for its first mailbox registration.
const SettingRelayAdminToken = "relay_admin_token"

// The daemon's relay registration state (daemon.SettingRelayRegistered and
// daemon.SettingRelayInvite), cleared when init moves to another relay.
const (
	settingRelayRegistered = "relay_registered"
	settingRelayInvite     = "relay_invite"
)

func init() { Register(newInitCmd) }

func newInitCmd(env *Env) *cobra.Command {
	var relay, token, name string
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Configure this machine (relay URL, device name, first-machine admin token)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := runInit(cmd.Context(), env, relay, token, name, force); err != nil {
				return err
			}
			fmt.Fprintln(env.Stdout, "Next: run `cravv-connect daemon install` (starts at login) or `cravv-connect daemon run`.")
			return nil
		},
	}
	cmd.Flags().StringVar(&relay, "relay", "", "relay URL, for example https://relay.example.com (required)")
	cmd.Flags().StringVar(&token, "relay-token", "", "relay admin token, only for the first machine; use - to read it from stdin")
	cmd.Flags().StringVar(&name, "name", "", "device name suggested to peers (default: host name)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config.toml")
	cmd.MarkFlagRequired("relay")
	return cmd
}

var nameClean = regexp.MustCompile(`[^a-z0-9-]+`)

// suggestAlias turns any name into [a-z0-9-], at most 24 characters.
func suggestAlias(s string) string {
	s = nameClean.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 24 {
		s = strings.TrimRight(s[:24], "-")
	}
	if s == "" {
		return "peer"
	}
	return s
}

// relayOrigin checks the relay URL and returns its normalized origin
// (relay-v1 section 1): http or https, a host and an optional port, nothing
// else. Relays sign and verify requests against this exact origin.
func relayOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (!strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http")) || u.Host == "" {
		return "", fmt.Errorf("relay must be an http or https URL, got %q", raw)
	}
	origin, err := relayproto.NormalizeOrigin(raw)
	if err != nil {
		return "", fmt.Errorf("relay must be scheme://host[:port] with no path, query or user: %w", err)
	}
	return origin, nil
}

func runInit(ctx context.Context, env *Env, relay, token, name string, force bool) error {
	relay, err := relayOrigin(relay)
	if err != nil {
		return err
	}
	paths, err := env.Paths()
	if err != nil {
		return err
	}
	oldRelay := ""
	if _, err := os.Stat(paths.Config); err == nil {
		if !force {
			return errors.New("already initialized (config.toml exists); use --force to overwrite")
		}
		if old, err := config.Load(paths); err == nil && old.RelayURL != "" {
			if o, err := relayOrigin(old.RelayURL); err == nil {
				oldRelay = o
			} else {
				oldRelay = old.RelayURL
			}
		}
	}
	movedRelay := oldRelay != "" && oldRelay != relay
	if name == "" {
		host, err := env.Hostname()
		if err != nil {
			host = "machine"
		}
		// "gpu-box.lan" or "Prith's MacBook.local": the name is the part
		// before the domain.
		host, _, _ = strings.Cut(host, ".")
		name = host
	}
	name = suggestAlias(name)
	if token == "-" {
		line, err := bufio.NewReader(env.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		token = strings.TrimSpace(line)
	}
	cfg := config.Defaults()
	cfg.RelayURL, cfg.DeviceName = relay, name
	if err := config.Save(paths, cfg); err != nil {
		return err
	}
	if token != "" || movedRelay {
		settings, closeFn, err := env.OpenSettings(paths.DB)
		if err != nil {
			return err
		}
		defer closeFn()
		if movedRelay {
			// The mailbox and any invite belong to the old relay: register again.
			for _, k := range []string{settingRelayRegistered, settingRelayInvite} {
				if err := settings.SetSetting(ctx, k, ""); err != nil {
					return err
				}
			}
		}
		if token != "" {
			if err := settings.SetSetting(ctx, SettingRelayAdminToken, token); err != nil {
				return err
			}
		}
	}
	w := env.Stdout
	fmt.Fprintf(w, "Initialized cravv-connect in %s\n", paths.Home)
	fmt.Fprintf(w, "Relay: %s\n", relay)
	fmt.Fprintf(w, "Device name: %s\n", name)
	if token != "" {
		fmt.Fprintln(w, "Admin token saved; the daemon registers this machine with the relay on first start.")
	}
	if movedRelay {
		fmt.Fprintf(w, "The relay changed from %s: this machine registers again on the new relay "+
			"(it needs --relay-token or an invite from a pairing). Your peers are not told about the move "+
			"(this version does not send control.relay_moved): re-pair with each of them on the new relay. "+
			"Restart the daemon to use it.\n", oldRelay)
	}
	return nil
}
