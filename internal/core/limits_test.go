package core

import (
	"testing"
	"time"
)

func TestLimitValues(t *testing.T) {
	ints := []struct {
		name      string
		got, want int64
	}{
		{"MaxTextBytes", MaxTextBytes, 65536},
		{"MaxFileBytes", MaxFileBytes, 104857600},
		{"FileChunkBytes", FileChunkBytes, 1048576},
		{"MaxFrameBytes", MaxFrameBytes, 262144},
		{"MailboxQueueBytes", MailboxQueueBytes, 52428800},
		{"MailboxQueueFrames", MailboxQueueFrames, 10000},
		{"LockoutFailures", LockoutFailures, 5},
		{"DefaultPeerQuota", DefaultPeerQuota, 1073741824},
	}
	for _, tt := range ints {
		if tt.got != tt.want {
			t.Errorf("%s = %d, want %d", tt.name, tt.got, tt.want)
		}
	}
	durs := []struct {
		name      string
		got, want time.Duration
	}{
		{"RelayTTL", RelayTTL, 168 * time.Hour},
		{"PrekeyRetention", PrekeyRetention, 504 * time.Hour},
		{"DedupWindow", DedupWindow, 720 * time.Hour},
		{"MaxMessageAge", MaxMessageAge, 504 * time.Hour},
		{"MaxClockSkew", MaxClockSkew, 10 * time.Minute},
		{"ReclaimGrace", ReclaimGrace, 5 * time.Minute},
		{"MaxWait", MaxWait, 50 * time.Second},
		{"BackoffMax", BackoffMax, 5 * time.Minute},
	}
	for _, tt := range durs {
		if tt.got != tt.want {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}
