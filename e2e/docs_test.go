package e2e

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/cookwithcravv/cravv-connect/internal/cli"
)

// readmeCLIReference returns the rows of the README's CLI reference table.
func readmeCLIReference(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(raw), "\n## CLI reference\n")
	if !ok {
		t.Fatal("README has no CLI reference section")
	}
	var rows []string
	started := false
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "| `") {
			rows = append(rows, line)
			started = true
		} else if started && !strings.HasPrefix(line, "|") {
			break
		}
	}
	if len(rows) == 0 {
		t.Fatal("the CLI reference has no rows")
	}
	return rows
}

// cliCommands returns every command a person can run, by its path without
// the program name ("link accept"), from the real command tree.
func cliCommands() map[string]*cobra.Command {
	out := map[string]*cobra.Command{}
	var walk func(prefix string, c *cobra.Command)
	walk = func(prefix string, c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Hidden {
				continue
			}
			path := strings.TrimSpace(prefix + " " + sub.Name())
			if sub.Runnable() {
				out[path] = sub
			}
			walk(path, sub)
		}
	}
	walk("", cli.NewRoot(&cli.Env{}))
	return out
}

var (
	codeSpan    = regexp.MustCompile("`([^`]+)`")
	commandWord = regexp.MustCompile(`^[a-z][a-z-]*$`)
)

// rowCommands returns the command words at the start of each code span in
// a row's first cell ("link permit" from "`link permit <link> <level>`").
func rowCommands(row string) []string {
	cells := strings.Split(row, " | ")
	var out []string
	for _, m := range codeSpan.FindAllStringSubmatch(cells[0], -1) {
		var words []string
		for _, w := range strings.Fields(m[1]) {
			if !commandWord.MatchString(w) {
				break
			}
			words = append(words, w)
		}
		out = append(out, strings.Join(words, " "))
	}
	return out
}

// The README's CLI reference names every command a person can run, with
// every flag it takes, and nothing that does not exist.
func TestREADMEDocumentsEveryCommand(t *testing.T) {
	t.Parallel()
	rows := readmeCLIReference(t)
	cmds := cliCommands()
	documented := map[string][]string{} // command path -> the rows naming it
	for _, row := range rows {
		for _, words := range rowCommands(row) {
			path, found := words, false
			for path != "" {
				if _, ok := cmds[path]; ok {
					documented[path] = append(documented[path], row)
					found = true
					break
				}
				path = strings.TrimSpace(path[:max(strings.LastIndex(path, " "), 0)])
			}
			if !found {
				t.Errorf("the CLI reference names %q, which is not a command", words)
			}
		}
	}
	for path, c := range cmds {
		rs, ok := documented[path]
		if !ok {
			t.Errorf("the CLI reference does not document `%s`", path)
			continue
		}
		text := strings.Join(rs, "\n")
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "help" {
				return
			}
			if !strings.Contains(text, "--"+f.Name) && (f.Shorthand == "" || !strings.Contains(text, "-"+f.Shorthand+" ") && !strings.Contains(text, "-"+f.Shorthand+"]")) {
				t.Errorf("the CLI reference for `%s` does not mention --%s", path, f.Name)
			}
		})
	}
}

// No Markdown file in the repository has an em dash.
func TestDocsHaveNoEmDashes(t *testing.T) {
	t.Parallel()
	var checked int
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "\u2014") {
				t.Errorf("%s:%d has an em dash: %s", path, i+1, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 5 {
		t.Fatalf("checked only %d Markdown files", checked)
	}
}
