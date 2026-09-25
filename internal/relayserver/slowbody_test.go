package relayserver

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A client that trickles a blob body must not hold the handler past the read deadline.
func TestBlobBodyReadDeadline(t *testing.T) {
	tr := newTestRelayCfg(t, func(c *Config) { c.BodyReadTimeout = 100 * time.Millisecond })
	conn, err := net.Dial("tcp", strings.TrimPrefix(tr.ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "PUT %s HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\n0123456789", chunkPath("abc", 0))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no response to a stalled body within 3s: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want 408", resp.StatusCode)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("took %v", el)
	}
}
