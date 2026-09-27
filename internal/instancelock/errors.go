package instancelock

import (
	"errors"
	"fmt"
)

var ErrHeld = errors.New("instance lock held")

type heldError struct{ path string }

func (e *heldError) Error() string {
	return fmt.Sprintf("Gorganizer's background service is already running (lock held at %s).", e.path)
}

func (e *heldError) Unwrap() error { return ErrHeld }
