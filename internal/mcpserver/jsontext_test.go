package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
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
	if w := desc["wait_for_message"]; !strings.Contains(w, "at most 600") || !strings.Contains(w, "default 50") {
		t.Errorf("wait_for_message: %q", w)
	}
	for _, pat := range []string{".env", "id_*", "credentials*.json", "service-account*.json", "*.pem", "*.key", "*.p12", "*.pfx", "*.jks", "*.keystore", "*.kdbx", "*.ppk", "*.ovpn"} {
		if !strings.Contains(desc["send_file"], pat) {
			t.Errorf("send_file description lacks %s: %q", pat, desc["send_file"])
		}
	}
}
