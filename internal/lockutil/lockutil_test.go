package lockutil

import (
	"errors"
	"testing"
)

func TestNamedLock(t *testing.T) {
	nls := &NamedLockSet{
		File: t.TempDir() + "/testlock",
	}
	nls.TryAcquire("test")
	if err := nls.TryAcquire("test"); !errors.Is(err, ErrLockAlreadyHeld) {
		t.Errorf("Expected ErrLockAlreadyHeld, got %v", err)
	}
	if err := nls.TryAcquire("test2"); err != nil {
		t.Errorf("Expected nil, got %v", err)
	}
	nls.Release("test")
	if err := nls.TryAcquire("test"); err != nil {
		t.Errorf("Expected nil, got %v", err)
	}
	nls.Release("test")
}
