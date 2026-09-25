package core

import (
	"errors"
	"fmt"
	"testing"
)

func TestSentinelErrorsDistinctAndWrappable(t *testing.T) {
	all := []error{ErrNotFound, ErrNotPermitted, ErrPaused, ErrPausedByPeer, ErrKilled, ErrAuthRequired,
		ErrLocked, ErrBadPassword, ErrAlreadyClaimed, ErrBadTransition, ErrTooLarge, ErrPathRefused, ErrQuota, ErrNoSession}
	for i, a := range all {
		wrapped := fmt.Errorf("context: %w", a)
		if !errors.Is(wrapped, a) {
			t.Errorf("%v does not survive wrapping", a)
		}
		for j, b := range all {
			if i != j && errors.Is(a, b) {
				t.Errorf("%v matches %v", a, b)
			}
		}
	}
}
