package daemon

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/config"
)

// TestHealthReportsIdentity verifies stable per-instance identity, the build version, and shutdown status.
func TestHealthReportsIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := newIsolatedDaemon(t, nil)
	initial := first.Health()
	if _, err := uuid.Parse(initial.InstanceID); err != nil {
		t.Fatalf("instance ID = %q: %v", initial.InstanceID, err)
	}
	if initial.PID != int32(os.Getpid()) || initial.APIEpoch != APIEpoch || initial.Stopping {
		t.Fatalf("initial identity = %+v", initial)
	}
	if again := first.Health(); again != initial {
		t.Fatalf("Health changed within an instance: %+v -> %+v", initial, again)
	}
	second, err := NewWithVersion(config.DefaultConfig(), "1.2.3-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Shutdown)
	other := second.Health()
	if other.InstanceID == initial.InstanceID || other.Version != "1.2.3-test" || other.PID != initial.PID || other.APIEpoch != APIEpoch {
		t.Fatalf("new instance identity = %+v, first = %+v", other, initial)
	}
	second.Shutdown()
	if stopped := second.Health(); !stopped.Stopping || stopped.InstanceID != other.InstanceID {
		t.Fatalf("shutdown identity = %+v, before = %+v", stopped, other)
	}
	if err := second.refuseWhenShuttingDown("test"); err == nil {
		t.Fatal("Health reports stopping but the daemon still accepts work")
	}
}
