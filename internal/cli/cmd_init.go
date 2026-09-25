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

	"github.com/cravv/cravv-connect/internal/config"
	"github.com/spf13/cobra"
)

// SettingRelayAdminToken is the settings key init stores the relay admin
// token under. The daemon reads it for its first mailbox registration.
const SettingRelayAdminToken = "relay_admin_token"

func init() { Register(newInitCmd) }

func newInitCmd(env *Env) *cobra.Command {
	var relay, token, name string
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Configure this machine (relay URL, device name, first-machine admin token)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd.Context(), env, relay, token, name, force)
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

func validRelayURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("relay must be an http or https URL, got %q", raw)
	}
	return nil
}

func runInit(ctx context.Context, env *Env, relay, token, name string, force bool) error {
	if err := validRelayURL(relay); err != nil {
		return err
	}
	paths, err := env.Paths()
	if err != nil {
		return err
	}
	if _, err := os.Stat(paths.Config); err == nil && !force {
		return errors.New("already initialized (config.toml exists); use --force to overwrite")
	}
	if name == "" {
		host, err := env.Hostname()
		if err != nil {
			host = "machine"
		}
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
	if token != "" {
		settings, closeFn, err := env.OpenSettings(paths.DB)
		if err != nil {
			return err
		}
		defer closeFn()
		if err := settings.SetSetting(ctx, SettingRelayAdminToken, token); err != nil {
			return err
		}
	}
	w := env.Stdout
	fmt.Fprintf(w, "Initialized cravv-connect in %s\n", paths.Home)
	fmt.Fprintf(w, "Relay: %s\n", relay)
	fmt.Fprintf(w, "Device name: %s\n", name)
	if token != "" {
		fmt.Fprintln(w, "Admin token saved; the daemon registers this machine with the relay on first start.")
	}
	fmt.Fprintln(w, "Next: run `cravv-connect daemon install` (starts at login) or `cravv-connect daemon run`.")
	return nil
}
