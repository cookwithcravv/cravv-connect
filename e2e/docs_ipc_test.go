package e2e

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/api"
	"github.com/cravv/cravv-connect/internal/app"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// ipcDoc is the local API's specification.
const ipcDoc = "../protocol/ipc-v1.md"

// The generated method table sits between these markers in ipcDoc.
const (
	ipcTableBegin = "<!-- ipc-methods: generated from the daemon's registry by TestIPCDocMatchesRegistry; CRAVV_UPDATE_DOCS=1 go test ./e2e -run TestIPCDocMatchesRegistry rewrites it -->"
	ipcTableEnd   = "<!-- /ipc-methods -->"
)

// ipcRegistry returns every method the daemon serves and its gates, from
// the registries app.Serve registers (api.NewServer, RegisterUI,
// RegisterManaged) on a real daemon.
func ipcRegistry(t *testing.T) map[string]ipc.Gate {
	t.Helper()
	n := NewNode(t, NewRelay(t), "docs", NodeOptions{})
	srv := api.NewServer(app.Ports(n.Daemon), n.Clock, nil)
	api.RegisterUI(srv, app.UIPorts(n.Daemon, nil))
	api.RegisterManaged(srv, app.ManagedPorts(n.Daemon))
	return srv.Methods()
}

// ipcMethodConstants returns the value of every Method* string constant in
// internal/ipc, so a method registered somewhere this test does not build
// still has to be documented.
func ipcMethodConstants(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("../internal/ipc/*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Method") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					if v != ipc.MethodCancel { // a protocol notification, not a method
						out = append(out, v)
					}
				}
			}
			return true
		})
	}
	slices.Sort(out)
	return out
}

// gateWords says what a method's gates ask of the calling connection.
func gateWords(g ipc.Gate) string {
	var needs []string
	if g&ipc.GateSession != 0 {
		needs = append(needs, "registered")
	}
	if g&ipc.GateShared != 0 {
		needs = append(needs, "shared session")
	}
	if g&ipc.GateUnlock != 0 {
		needs = append(needs, "password")
	}
	if len(needs) == 0 {
		return "nothing"
	}
	return strings.Join(needs, ", ")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// ipcMethodTable renders the generated table for the registry.
func ipcMethodTable(methods map[string]ipc.Gate) string {
	names := make([]string, 0, len(methods))
	for m := range methods {
		names = append(names, m)
	}
	slices.Sort(names)
	var b strings.Builder
	b.WriteString(ipcTableBegin + "\n\n")
	b.WriteString("| Method | Gate | While killed | From a managed run |\n|---|---|---|---|\n")
	for _, m := range names {
		g := methods[m]
		b.WriteString("| `" + m + "` | " + gateWords(g) + " | " + yesNo(g&ipc.GateAllowWhenKilled != 0) + " | " + yesNo(ipc.RunMethods[m]) + " |\n")
	}
	b.WriteString("\n" + ipcTableEnd)
	return b.String()
}

var ipcTableBlock = regexp.MustCompile(`(?s)<!-- ipc-methods: .*?<!-- /ipc-methods -->`)

// The IPC spec lists every method the daemon serves, with the gates it
// really has (the generated table), and documents each one's params and
// result in the method reference (a row starting "| `method` |"). Nothing
// is documented that the daemon does not serve.
func TestIPCDocMatchesRegistry(t *testing.T) {
	t.Parallel()
	methods := ipcRegistry(t)
	for _, c := range ipcMethodConstants(t) {
		if _, ok := methods[c]; !ok {
			t.Errorf("ipc declares method %q, but the daemon does not register it", c)
		}
	}
	raw, err := os.ReadFile(ipcDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	want := ipcMethodTable(methods)
	if os.Getenv("CRAVV_UPDATE_DOCS") == "1" {
		if !ipcTableBlock.MatchString(doc) {
			t.Fatalf("%s has no generated method table to update", ipcDoc)
		}
		doc = ipcTableBlock.ReplaceAllLiteralString(doc, want)
		if err := os.WriteFile(ipcDoc, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := ipcTableBlock.FindString(doc); got != want {
		t.Errorf("the method table in %s is not what the registry says; run CRAVV_UPDATE_DOCS=1 go test ./e2e -run TestIPCDocMatchesRegistry\n got:\n%s\nwant:\n%s", ipcDoc, got, want)
	}

	ref := ipcTableBlock.ReplaceAllString(doc, "")
	start := strings.Index(ref, "\n## 6. Method reference")
	if start < 0 {
		t.Fatalf("%s has no section 6. Method reference", ipcDoc)
	}
	ref = ref[start+1:]
	if end := strings.Index(ref, "\n## 7."); end >= 0 {
		ref = ref[:end]
	}
	row := regexp.MustCompile("(?m)^\\| `([a-z_$/.]+)` \\|")
	documented := map[string]bool{}
	for _, m := range row.FindAllStringSubmatch(ref, -1) {
		documented[m[1]] = true
		if _, ok := methods[m[1]]; !ok {
			t.Errorf("the method reference documents %q, which the daemon does not serve", m[1])
		}
	}
	for m := range methods {
		if !documented[m] {
			t.Errorf("the method reference does not document %q", m)
		}
	}
}
