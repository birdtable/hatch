//go:build linux || darwin

package budget

import (
	"context"
	"math"
	"testing"

	"github.com/birdtable/go-proc/load"
)

// HostLoad is go-proc/load's figure for the window asked, 1, 5 or 15 minutes: the
// load drifts between two reads, so the two are within a margin of a unit.
func TestHostLoadIsLoadsFigure(t *testing.T) {
	for _, window := range []int{1, 5, 15} {
		got, cpus, err := HostLoad(context.Background(), window)
		if err != nil {
			t.Fatal(err)
		}
		r, err := load.Read()
		if err != nil {
			t.Fatal(err)
		}
		if cpus != r.CPUs || cpus < 1 {
			t.Fatalf("window %d: HostLoad says %d CPUs, load.Read %d", window, cpus, r.CPUs)
		}
		want := map[int]float64{1: r.One, 5: r.Five, 15: r.Fifteen}[window]
		if math.Abs(got-want) > 1 {
			t.Fatalf("window %d: HostLoad %.2f, load.Read %.2f", window, got, want)
		}
	}
}
