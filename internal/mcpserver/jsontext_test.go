package mcpserver

import (
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
