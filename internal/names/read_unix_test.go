//go:build unix

package names

import (
	"os"
	"path/filepath"
	"testing"
)

// What a link, an owner and a mode are is Unix's; the Windows session judges
// owners and ACLs of its own.

func TestAListThroughALinkIsRefused(t *testing.T) {
	eachFlag(t, func(t *testing.T, flag string) {
		target := plant(t, flag)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("no link: %v", err)
		}
		refusal(t, flag, link)
	})
}

func TestAListAnotherAccountOwnsIsRefused(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("another account's file needs chown, which only root may do; run as root to plant it")
	}
	eachFlag(t, func(t *testing.T, flag string) {
		p := plant(t, flag)
		if err := os.Chown(p, 12345, 12345); err != nil {
			t.Skipf("chown refused: %v", err)
		}
		refusal(t, flag, p)
	})
}

func TestAListAnotherAccountCanWriteIsRefused(t *testing.T) {
	for _, mode := range []os.FileMode{0o666, 0o662, 0o626} {
		eachFlag(t, func(t *testing.T, flag string) {
			p := plant(t, flag)
			if err := os.Chmod(p, mode); err != nil {
				t.Fatal(err)
			}
			refusal(t, flag, p)
		})
	}
}
