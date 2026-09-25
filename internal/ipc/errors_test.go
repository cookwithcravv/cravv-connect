package ipc

import (
	"errors"
	"fmt"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
)

func TestErrorMappingRoundTrip(t *testing.T) {
	for _, tc := range errorKinds {
		t.Run(tc.kind, func(t *testing.T) {
			wrapped := fmt.Errorf("doing thing: %w", tc.err)
			if got := KindOf(wrapped); got != tc.kind {
				t.Fatalf("KindOf = %q, want %q", got, tc.kind)
			}
			back := fromWire(toWire(wrapped))
			if !errors.Is(back, tc.err) {
				t.Fatalf("round trip lost sentinel: %v", back)
			}
			if back.Error() != wrapped.Error() {
				t.Fatalf("message = %q, want %q", back.Error(), wrapped.Error())
			}
		})
	}
}

func TestPausedAndPausedByPeerStayDistinct(t *testing.T) {
	if KindOf(core.ErrPausedByPeer) != KindPausedByPeer {
		t.Fatal("paused_by_peer mapped wrong")
	}
	if errors.Is(fromWire(toWire(core.ErrPaused)), core.ErrPausedByPeer) {
		t.Fatal("paused must not match paused_by_peer")
	}
}

func TestUnknownErrorIsInternal(t *testing.T) {
	w := toWire(errors.New("disk on fire"))
	if w.Code != CodeServerError || w.Data.Kind != KindInternal || w.Message != "disk on fire" {
		t.Fatalf("got %+v", w)
	}
	back := fromWire(w)
	if errors.Unwrap(back) != nil {
		t.Fatalf("internal error must not unwrap to a sentinel")
	}
}

func TestBadRequestUsesInvalidParamsCode(t *testing.T) {
	w := toWire(fmt.Errorf("%w: missing to", ErrBadRequest))
	if w.Code != CodeInvalidParams {
		t.Fatalf("code = %d", w.Code)
	}
	if back := fromWire(&Error{Code: CodeMethodNotFound, Message: "no such method"}); !errors.Is(back, ErrBadRequest) {
		t.Fatalf("method-not-found without data should map to bad_request, got %v", back)
	}
}

func TestRegisterErrorKindBothDirections(t *testing.T) {
	errOffline := errors.New("relay offline")
	RegisterErrorKind(errOffline, "offline")
	RegisterErrorKind(errOffline, "offline") // idempotent
	w := toWire(fmt.Errorf("pairing: %w", errOffline))
	if w.Data.Kind != "offline" || w.Code != CodeServerError {
		t.Fatalf("got %+v", w)
	}
	back := fromWire(w)
	if !errors.Is(back, errOffline) || !IsKind(back, "offline") {
		t.Fatalf("round trip: %v", back)
	}
}

func TestUnknownKindKeepsKindAndMessage(t *testing.T) {
	back := fromWire(&Error{Code: CodeServerError, Message: "someone else is pairing", Data: &ErrorData{Kind: "busy"}})
	if !IsKind(back, "busy") || back.Error() != "someone else is pairing" || errors.Unwrap(back) != nil {
		t.Fatalf("got %#v", back)
	}
}
