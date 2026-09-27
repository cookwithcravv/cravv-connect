// Package config resolves the state directory and loads/saves config.toml.
// config.toml is parsed by a small hand-written parser that understands only
// `key = "string"` and `key = 123` lines, blank lines and # comments.
package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/cookwithcravv/cravv-connect/internal/auth"
	"github.com/cookwithcravv/cravv-connect/internal/core"
)

// Config is the user-editable configuration.
type Config struct {
	RelayURL   string `toml:"relay_url"`
	DeviceName string `toml:"device_name"`
	PeerQuota  int64  `toml:"peer_quota"`
	PAMService string `toml:"pam_service"`
}

// Paths are the locations of every file in the state directory.
type Paths struct{ Home, Config, DB, Audit, Socket, Files, Log string }

// PIDFile is where a running daemon records its process ID.
func (p Paths) PIDFile() string { return filepath.Join(p.Home, "daemon.pid") }

// StderrLog catches the daemon's stderr when it runs in the background: only
// output written before logging starts, or a crash. The log proper is Log,
// which the daemon rotates itself.
func (p Paths) StderrLog() string { return filepath.Join(p.Home, "daemon-stderr.log") }

// EnvHome overrides the state directory (used by tests and multi-daemon setups).
const EnvHome = "CRAVV_HOME"

// ResolvePaths returns the paths under $CRAVV_HOME or ~/.cravv-connect,
// creating the directory with mode 0700 (and tightening it if it exists).
func ResolvePaths() (Paths, error) {
	home := os.Getenv(EnvHome)
	if home == "" {
		uh, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("config: home directory: %w", err)
		}
		home = filepath.Join(uh, ".cravv-connect")
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return Paths{}, fmt.Errorf("config: state directory: %w", err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return Paths{}, fmt.Errorf("config: create %s: %w", home, err)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return Paths{}, fmt.Errorf("config: chmod %s: %w", home, err)
	}
	return Paths{
		Home:   home,
		Config: filepath.Join(home, "config.toml"),
		DB:     filepath.Join(home, "store.db"),
		Audit:  filepath.Join(home, "audit.log"),
		Socket: filepath.Join(home, "daemon.sock"),
		Files:  filepath.Join(home, "files"),
		Log:    filepath.Join(home, "daemon.log"),
	}, nil
}

// Defaults returns the configuration used for keys the file does not set.
func Defaults() Config {
	name, err := os.Hostname()
	if err != nil {
		name = "machine"
	}
	if i := strings.IndexByte(name, '.'); i > 0 {
		name = name[:i]
	}
	return Config{
		DeviceName: name,
		PeerQuota:  core.DefaultPeerQuota,
		PAMService: auth.DefaultPAMService(),
	}
}

// Load reads p.Config over Defaults. A missing file yields Defaults.
// Unknown keys are ignored; malformed lines are errors naming the line.
// A peer_quota <= 0 is an error. A pam_service that is set but not allowlisted by auth is an error
// matching auth.ErrServiceNotAllowed.
func Load(p Paths) (Config, error) {
	c := Defaults()
	data, err := os.ReadFile(p.Config)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: read: %w", err)
	}
	fields := fieldsByKey(&c)
	set := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, fmt.Errorf("config: %s line %d: expected key = value", p.Config, n)
		}
		key = strings.TrimSpace(key)
		f, known := fields[key]
		if !known {
			continue
		}
		if err := setField(f, strings.TrimSpace(raw)); err != nil {
			return Config{}, fmt.Errorf("config: %s line %d (%s): %w", p.Config, n, key, err)
		}
		set[key] = true
	}
	if err := sc.Err(); err != nil {
		return Config{}, fmt.Errorf("config: read: %w", err)
	}
	if err := validate(c, set, p.Config); err != nil {
		return Config{}, err
	}
	return c, nil
}

// validate checks the values of the keys the file set.
func validate(c Config, set map[string]bool, path string) error {
	if set["peer_quota"] && c.PeerQuota <= 0 {
		return fmt.Errorf("config: %s: peer_quota must be a positive number of bytes, got %d", path, c.PeerQuota)
	}
	if set["pam_service"] {
		if err := auth.CheckPAMService(c.PAMService); err != nil {
			return fmt.Errorf("config: %s: pam_service: %w", path, err)
		}
	}
	return nil
}

// Save writes c to p.Config atomically with mode 0600.
func Save(p Paths, c Config) error {
	var b strings.Builder
	b.WriteString("# cravv-connect configuration\n")
	v := reflect.ValueOf(c)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		key := t.Field(i).Tag.Get("toml")
		switch f := v.Field(i); f.Kind() {
		case reflect.String:
			fmt.Fprintf(&b, "%s = %s\n", key, strconv.Quote(f.String()))
		case reflect.Int64:
			fmt.Fprintf(&b, "%s = %d\n", key, f.Int())
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(p.Config), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("config: save: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("config: save: %w", err)
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return fmt.Errorf("config: save: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: save: %w", err)
	}
	if err := os.Rename(tmp.Name(), p.Config); err != nil {
		return fmt.Errorf("config: save: %w", err)
	}
	return nil
}

// fieldsByKey maps each `toml` tag to its settable field.
func fieldsByKey(c *Config) map[string]reflect.Value {
	v := reflect.ValueOf(c).Elem()
	t := v.Type()
	m := make(map[string]reflect.Value, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		m[t.Field(i).Tag.Get("toml")] = v.Field(i)
	}
	return m
}

// setField parses raw (the text after '=') into f.
func setField(f reflect.Value, raw string) error {
	switch f.Kind() {
	case reflect.String:
		s, rest, err := parseQuoted(raw)
		if err != nil {
			return err
		}
		if err := onlyComment(rest); err != nil {
			return err
		}
		f.SetString(s)
	case reflect.Int64:
		num, _, _ := strings.Cut(raw, "#")
		n, err := strconv.ParseInt(strings.ReplaceAll(strings.TrimSpace(num), "_", ""), 10, 64)
		if err != nil {
			return fmt.Errorf("expected an integer, got %q", strings.TrimSpace(num))
		}
		f.SetInt(n)
	default:
		return fmt.Errorf("unsupported field kind %s", f.Kind())
	}
	return nil
}

// parseQuoted reads a double-quoted string with backslash escapes from the
// start of raw and returns it plus the remaining text.
func parseQuoted(raw string) (string, string, error) {
	if !strings.HasPrefix(raw, `"`) {
		return "", "", errors.New(`expected a "quoted string"`)
	}
	for i := 1; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			i++
		case '"':
			s, err := strconv.Unquote(raw[:i+1])
			if err != nil {
				return "", "", fmt.Errorf("bad string %s: %w", raw[:i+1], err)
			}
			return s, raw[i+1:], nil
		}
	}
	return "", "", errors.New("unterminated string")
}

func onlyComment(rest string) error {
	rest = strings.TrimSpace(rest)
	if rest == "" || strings.HasPrefix(rest, "#") {
		return nil
	}
	return fmt.Errorf("unexpected text after value: %q", rest)
}
