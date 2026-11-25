//go:build windows

package budget

import (
	"context"
	"errors"
	"testing"

	"github.com/birdtable/go-proc/load"
)

// Windows keeps no load average: HostLoad says the load is unknown, typed, with
// go-proc's own refusal inside it, and the run prints that reason.
func TestHostLoadIsUnknownOnWindows(t *testing.T) {
	_, _, err := HostLoad(context.Background(), 1)
	if !errors.Is(err, ErrNoLoadAverage) || !errors.Is(err, load.ErrNoAverage) {
		t.Fatalf("got %v", err)
	}
}
