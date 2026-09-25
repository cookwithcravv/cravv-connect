package store

import "testing"

// Setting keys are persisted in user databases; renaming one silently loses state.
func TestSettingKeysAreStable(t *testing.T) {
	cases := map[string]string{
		SettingKilled:       "killed",
		SettingAllowPaths:   "allow_paths",
		SettingIdentitySeed: "identity_seed_b64",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("setting key %q, want %q", got, want)
		}
	}
}

// Status strings are persisted too.
func TestPersistedEnumValuesAreStable(t *testing.T) {
	pairs := [][2]string{
		{string(OutboxPending), "pending"}, {string(OutboxQueued), "queued"}, {string(OutboxHeld), "held"},
		{string(TaskInbound), "in"}, {string(TaskOutbound), "out"},
		{string(FileOffered), "offered"}, {string(FileHeld), "held"}, {string(FileDownloading), "downloading"},
		{string(FileDone), "done"}, {string(FileFailed), "failed"}, {string(FileDeclined), "declined"},
		{string(FileUploading), "uploading"}, {string(FileSent), "sent"},
	}
	for _, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("got %q, want %q", p[0], p[1])
		}
	}
}
