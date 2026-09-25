package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/ipc"
)

func TestJSONTextKeepsWrapperReadable(t *testing.T) {
	out, err := jsonText(ipc.TaskView{TaskID: "T", Wrapped: "<remote_message from=\"a\">\n&lt;b&gt;\n</remote_message>"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `<remote_message from=\"a\">`) || strings.Contains(out, `\u003c`) || strings.HasSuffix(out, "\n") {
		t.Fatalf("jsonText = %s", out)
	}
}

func TestToolDescriptionsMatchBehaviour(t *testing.T) {
	d := newDaemonFake(t)
	d.start()
	cs, _ := connect(t, d, "claude-code")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	desc := map[string]string{}
	for _, tool := range res.Tools {
		desc[tool.Name] = tool.Description
	}
	if p := desc["pause_peer"]; strings.Contains(p, "Only the human can resume") || !strings.Contains(p, "cravv-connect resume-peer") {
		t.Errorf("pause_peer: %q", p)
	}
	for _, pat := range []string{".env", "id_*", "credentials*.json", "service-account*.json", "*.pem", "*.key", "*.p12", "*.pfx", "*.jks", "*.keystore", "*.kdbx", "*.ppk", "*.ovpn"} {
		if !strings.Contains(desc["send_file"], pat) {
			t.Errorf("send_file description lacks %s: %q", pat, desc["send_file"])
		}
	}
}
