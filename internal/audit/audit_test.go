package audit

import "testing"

func TestNopRecordsNothing(t *testing.T) {
	var l Logger = Nop{}
	if err := l.Record(Event{Type: EvKill}); err != nil {
		t.Fatalf("Nop.Record: %v", err)
	}
}

func TestEventTypeStrings(t *testing.T) {
	// These strings are persisted in audit.log and shown by `cravv-connect log`.
	want := map[string]string{
		EvPair: "pair", EvUnpair: "unpair", EvTrust: "trust", EvPause: "pause", EvResume: "resume",
		EvKill: "kill", EvKillResume: "kill_resume", EvApprove: "approve", EvDeny: "deny",
		EvPassword: "password_attempt", EvTaskIn: "task_in", EvFileIn: "file_in", EvFileOut: "file_out",
		EvAllowPath: "allow_path", EvResetIdentity: "reset_identity", EvFileAccept: "file_accept",
		EvLinkRequest: "link_request", EvLinkAccept: "link_accept", EvLinkReject: "link_reject",
		EvLinkClose: "link_close", EvLinkPermission: "link_permission",
	}
	for got, w := range want {
		if got != w {
			t.Errorf("event type %q, want %q", got, w)
		}
	}
}
