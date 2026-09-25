package core

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewEnvelopeFields(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC))
	env, err := NewEnvelope(clock, "from", "to", KindChat, ChatBody{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if env.V != 1 {
		t.Errorf("V = %d, want 1", env.V)
	}
	if !idPattern.MatchString(env.ID) {
		t.Errorf("ID = %q, not a NewID", env.ID)
	}
	if env.TS != clock.Now().UnixMilli() {
		t.Errorf("TS = %d, want %d", env.TS, clock.Now().UnixMilli())
	}
	if env.FromMachine != "from" || env.ToMachine != "to" || env.Kind != KindChat {
		t.Errorf("unexpected envelope %+v", env)
	}
	if string(env.Body) != `{"text":"hi"}` {
		t.Errorf("Body = %s", env.Body)
	}
}

func TestNewEnvelopeNilBody(t *testing.T) {
	env, err := NewEnvelope(NewFakeClock(time.UnixMilli(1)), "a", "b", KindControlPaused, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(env.Body) != `{}` {
		t.Fatalf("Body = %s, want {}", env.Body)
	}
}

func TestNewEnvelopeUnmarshalableBody(t *testing.T) {
	if _, err := NewEnvelope(SystemClock{}, "a", "b", KindChat, make(chan int)); err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestEnvelopeJSONRoundTrip(t *testing.T) {
	bodies := []struct {
		kind Kind
		body any
		into any
	}{
		{KindChat, ChatBody{Text: "hello <world>"}, &ChatBody{}},
		{KindTaskCreate, TaskCreateBody{TaskID: "T1", Instructions: "run tests", Files: []FileRef{{FileID: "F1", Name: "a.txt", Size: 3}}}, &TaskCreateBody{}},
		{KindTaskUpdate, TaskUpdateBody{TaskID: "T1", State: TaskDone, Result: "ok"}, &TaskUpdateBody{}},
		{KindTaskCancel, TaskCancelBody{TaskID: "T1"}, &TaskCancelBody{}},
		{KindFileOffer, FileOfferBody{FileID: "F", BlobID: "B", Name: "n", Size: 10, Chunks: 1, SHA256: []byte{1, 2}, Key: []byte{3}}, &FileOfferBody{}},
		{KindControlPrekey, PrekeyBody{Prekey: SignedPrekeyWire{ID: "P", Pub: []byte{9}, CreatedAt: 5, Sig: []byte{8}}}, &PrekeyBody{}},
		{KindControlStalePrekey, StalePrekeyBody{MsgID: "M", Prekey: SignedPrekeyWire{ID: "P"}}, &StalePrekeyBody{}},
		{KindControlDelivered, DeliveredBody{IDs: []string{"a", "b"}}, &DeliveredBody{}},
		{KindControlRelayMoved, RelayMovedBody{RelayURL: "https://r.example"}, &RelayMovedBody{}},
	}
	clock := NewFakeClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	for _, b := range bodies {
		env, err := NewEnvelope(clock, "m1", "m2", b.kind, b.body)
		if err != nil {
			t.Fatal(err)
		}
		env.FromSession = "claude@proj"
		env.ToSession = "codex@repo"
		// Encode as sealing does (no HTML escaping); json.Marshal would
		// re-escape the RawMessage body and change its bytes.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(env); err != nil {
			t.Fatal(err)
		}
		raw := buf.Bytes()
		var back Envelope
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(env, back) {
			t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", back, env)
		}
		if err := json.Unmarshal(back.Body, b.into); err != nil {
			t.Fatal(err)
		}
		if got := reflect.ValueOf(b.into).Elem().Interface(); !reflect.DeepEqual(got, b.body) {
			t.Fatalf("%s body mismatch: got %+v want %+v", b.kind, got, b.body)
		}
	}
}

func TestEnvelopeJSONFieldNames(t *testing.T) {
	env := Envelope{V: 1, ID: "I", TS: 7, FromMachine: "f", FromSession: "fs", ToMachine: "t", ToSession: "ts", Kind: KindChat, Body: json.RawMessage(`{}`)}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"v":1,"id":"I","ts":7,"from_machine":"f","from_session":"fs","to_machine":"t","to_session":"ts","kind":"chat","body":{}}`
	if string(raw) != want {
		t.Fatalf("json = %s\nwant   %s", raw, want)
	}
	env.FromSession, env.ToSession = "", ""
	raw, _ = json.Marshal(env)
	if strings.Contains(string(raw), "session") {
		t.Fatalf("empty sessions must be omitted: %s", raw)
	}
}

func TestNewEnvelopeIDMatchesTS(t *testing.T) {
	at := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	env, err := NewEnvelope(NewFakeClock(at), "a", "b", KindChat, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := newIDAt(time.UnixMilli(env.TS), bytes.NewReader(make([]byte, 10)))
	if env.ID[:9] != want[:9] {
		t.Fatalf("envelope ID %q does not encode TS %d (want prefix %q)", env.ID, env.TS, want[:9])
	}
}

func TestNewEnvelopeBodyNotHTMLEscaped(t *testing.T) {
	env, err := NewEnvelope(NewFakeClock(time.Unix(0, 0)), "a", "b", KindChat, ChatBody{Text: "<a&b>"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(env.Body); got != `{"text":"<a&b>"}` {
		t.Fatalf("body = %s", got)
	}
}
