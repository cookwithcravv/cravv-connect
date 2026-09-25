package sealing

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
)

func sampleFrame() Frame {
	return Frame{
		Header:  Header{V: 1, ID: "ID1", FromMachine: "from", ToMachine: "to", PKID: "PK1", Suite: DefaultSuite},
		Payload: []byte{1, 2, 3},
		Sig:     []byte{4, 5, 6},
	}
}

func TestFrameMarshalParseRoundTrip(t *testing.T) {
	f := sampleFrame()
	b, err := f.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"h":{"v":1,"id":"ID1","from_machine":"from","to_machine":"to","pk_id":"PK1","suite":"`) {
		t.Fatalf("unexpected JSON %s", b)
	}
	back, err := ParseFrame(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, f) {
		t.Fatalf("round trip: got %+v want %+v", back, f)
	}
}

func TestParseFrameRejects(t *testing.T) {
	mk := func(mut func(*Frame)) []byte {
		f := sampleFrame()
		mut(&f)
		b, _ := f.Marshal()
		return b
	}
	tests := []struct {
		name string
		in   []byte
	}{
		{"garbage", []byte("not json")},
		{"version 2", mk(func(f *Frame) { f.Header.V = 2 })},
		{"no id", mk(func(f *Frame) { f.Header.ID = "" })},
		{"no from", mk(func(f *Frame) { f.Header.FromMachine = "" })},
		{"no to", mk(func(f *Frame) { f.Header.ToMachine = "" })},
		{"no pk_id", mk(func(f *Frame) { f.Header.PKID = "" })},
		{"no suite", mk(func(f *Frame) { f.Header.Suite = "" })},
		{"no payload", mk(func(f *Frame) { f.Payload = nil })},
		{"no sig", mk(func(f *Frame) { f.Sig = nil })},
	}
	for _, tt := range tests {
		if _, err := ParseFrame(tt.in); err == nil {
			t.Errorf("%s: ParseFrame succeeded", tt.name)
		}
	}
	big := bytes.Repeat([]byte{' '}, core.MaxFrameBytes+1)
	if _, err := ParseFrame(big); !errors.Is(err, core.ErrTooLarge) {
		t.Errorf("oversized frame: err = %v, want ErrTooLarge", err)
	}
}

func TestSigningBytesExact(t *testing.T) {
	h := Header{V: 1, ID: "I", FromMachine: "f", ToMachine: "t", PKID: "p", Suite: "s"}
	got := string(signingBytes(h, []byte("PAYLOAD")))
	want := "cravv-frame-v1\n" + `{"v":1,"id":"I","from_machine":"f","to_machine":"t","pk_id":"p","suite":"s"}` + "\nPAYLOAD"
	if got != want {
		t.Fatalf("signing bytes = %q\nwant %q", got, want)
	}
	if info := string(h.info()); info != "cravv-connect/peer-v1\n"+`{"v":1,"id":"I","from_machine":"f","to_machine":"t","pk_id":"p","suite":"s"}` {
		t.Fatalf("info = %q", info)
	}
}
