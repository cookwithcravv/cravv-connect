package sealing

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

var (
	machineA = core.MachineID(strings.Repeat("a", 52))
	machineB = core.MachineID(strings.Repeat("b2", 26))
)

func sampleFrame() Frame {
	return Frame{
		Header:  Header{V: 1, ID: "ID1", FromMachine: machineA, ToMachine: machineB, PKID: "PK1", Suite: DefaultSuite},
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
	if !strings.Contains(string(b), `"h":{"v":1,"id":"ID1","from_machine":"`+string(machineA)+`","to_machine":"`+string(machineB)+`","pk_id":"PK1","suite":"`) {
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
	malformed := []struct {
		name string
		mut  func(*Frame)
	}{
		{"id with dash", func(f *Frame) { f.Header.ID = "ID-1" }},
		{"id non-ascii", func(f *Frame) { f.Header.ID = "ID\u00e91" }},
		{"id too long", func(f *Frame) { f.Header.ID = strings.Repeat("A", 65) }},
		{"pk_id with slash", func(f *Frame) { f.Header.PKID = "../x" }},
		{"pk_id too long", func(f *Frame) { f.Header.PKID = strings.Repeat("A", 65) }},
		{"from short", func(f *Frame) { f.Header.FromMachine = machineA[:51] }},
		{"from long", func(f *Frame) { f.Header.FromMachine = machineA + "a" }},
		{"from upper", func(f *Frame) { f.Header.FromMachine = core.MachineID(strings.Repeat("A", 52)) }},
		{"from digit 1", func(f *Frame) { f.Header.FromMachine = core.MachineID(strings.Repeat("1", 52)) }},
		{"to digit 8", func(f *Frame) { f.Header.ToMachine = core.MachineID(strings.Repeat("8", 52)) }},
		{"suite control", func(f *Frame) { f.Header.Suite = "x\ny" }},
		{"suite non-ascii", func(f *Frame) { f.Header.Suite = "su\u00efte" }},
		{"suite too long", func(f *Frame) { f.Header.Suite = strings.Repeat("s", 65) }},
		{"version 2", func(f *Frame) { f.Header.V = 2 }},
		{"no id", func(f *Frame) { f.Header.ID = "" }},
	}
	for _, tt := range malformed {
		if _, err := ParseFrame(mk(tt.mut)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: ParseFrame = %v, want ErrMalformed", tt.name, err)
		}
	}
	for _, ok := range []func(*Frame){
		func(f *Frame) { f.Header.ID = strings.Repeat("z", 64) },
		func(f *Frame) { f.Header.PKID = "a" },
		func(f *Frame) { f.Header.Suite = "~" },
		func(f *Frame) { f.Header.FromMachine = core.MachineID(strings.Repeat("7", 52)) },
	} {
		if _, err := ParseFrame(mk(ok)); err != nil {
			t.Errorf("valid header rejected: %v", err)
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
