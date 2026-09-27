package conformance

import (
	"fmt"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

// queueCapCases fill a mailbox to its caps. They run only with CRAVV_CONFORMANCE_SLOW=1.
func queueCapCases() []testCase {
	return []testCase{
		{"queue/full_by_frame_count", func(t *testing.T, s *suite) {
			slow(t)
			a, amb, _, bmb := s.pair(t)
			amb.Close()
			for i := range core.MailboxQueueFrames {
				if st := send(t, bmb, a, fmt.Sprintf("f%d", i), []byte{1}); st != transport.SendQueued {
					t.Fatalf("frame %d: %s", i, st)
				}
			}
			if st := send(t, bmb, a, "overflow", []byte{1}); st != transport.SendQueueFull {
				t.Fatalf("frame %d: %s, want queue_full", core.MailboxQueueFrames+1, st)
			}
		}},
		{"queue/full_by_bytes", func(t *testing.T, s *suite) {
			slow(t)
			a, amb, _, bmb := s.pair(t)
			amb.Close()
			n := core.MailboxQueueBytes / core.MaxFrameBytes // 200 frames fill 50 MB exactly
			frame := make([]byte, core.MaxFrameBytes)
			for i := range n {
				if st := send(t, bmb, a, fmt.Sprintf("b%d", i), frame); st != transport.SendQueued {
					t.Fatalf("frame %d: %s", i, st)
				}
			}
			if st := send(t, bmb, a, "overflow", []byte{1}); st != transport.SendQueueFull {
				t.Fatalf("one byte over: %s, want queue_full", st)
			}
		}},
	}
}
