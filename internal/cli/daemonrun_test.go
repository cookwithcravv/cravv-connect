package cli

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/config"
)

func TestDaemonRunLogFile(t *testing.T) {
	fd := newFakeDaemon(t)
	env, _, errb := fd.env(&fakePrompter{}, "")
	env.RunDaemon = func(ctx context.Context, paths config.Paths, logger *slog.Logger) error {
		logger.Info("hello from the daemon")
		return nil
	}
	logPath := filepath.Join(fd.home, "custom.log")
	if code := Main([]string{"daemon", "run", "--log-file", logPath}, env); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	b, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(b), "hello from the daemon") || !strings.Contains(string(b), "daemon starting") {
		t.Fatalf("log file %q, %v", b, err)
	}
	if strings.Contains(errb.String(), "hello from the daemon") {
		t.Fatal("logged to stderr as well as the file")
	}
}

func TestDaemonRunLogsFailureToFile(t *testing.T) {
	fd := newFakeDaemon(t)
	env, _, _ := fd.env(&fakePrompter{}, "")
	env.RunDaemon = func(context.Context, config.Paths, *slog.Logger) error {
		return os.ErrPermission
	}
	logPath := filepath.Join(fd.home, "d.log")
	if code := Main([]string{"daemon", "run", "--log-file", logPath}, env); code == 0 {
		t.Fatal("want failure")
	}
	if b, _ := os.ReadFile(logPath); !strings.Contains(string(b), "daemon exited") {
		t.Fatalf("failure not in log file: %q", b)
	}
}
