//go:build unix

package budget

import (
	"strings"
	"testing"
)

// On Windows a job object holds the grandchild, and TestNoJobObjectIsATreeNotRead holds that system's case.
// A tree that cannot be read whole is named and never passes: on Unix a grandchild that outlived the
// command, which its parent did not wait for.
func TestAnOrphanedGrandchildIsATreeNotRead(t *testing.T) {
	code, out, errb := judgeCPU(t, "orphan", 0, nil, helper("orphan"))
	if code != exitFailed || !strings.Contains(errb, "tree not read: a descendant outlived the tier") || strings.Contains(out, "used") {
		t.Fatalf("got %d, out %q, err %q", code, out, errb)
	}
}
