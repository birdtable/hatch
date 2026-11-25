package tag

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/birdtable/go-proc/proc"

	"github.com/birdtable/hatch/internal/gitrun"
)

// TestMain lets this test binary stand in for git's upload-pack as a remote's: a remote whose uploadpack is this
// binary with "flaky-upload-pack <counter file> <repository>" answers its first read as git does and fails every
// read after it with 128, as a remote that serves one question and then breaks does. The protocol is version 0,
// where the server states its refs first and the client reads them and goes, so the answer is one write.
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "flaky-upload-pack" {
		os.Exit(flakyUploadPack(os.Args[2], os.Args[3]))
	}
	os.Exit(m.Run())
}

func flakyUploadPack(counter, repo string) int {
	if _, err := os.Stat(counter); err == nil {
		fmt.Fprintln(os.Stderr, "fatal: the remote answers one read, and no more")
		return 128
	}
	if err := os.WriteFile(counter, []byte("served\n"), 0o600); err != nil {
		return 96
	}
	p, err := gitrun.Find()
	if err != nil {
		return 97
	}
	res, err := proc.Run(context.Background(), proc.Cmd{
		Path:      p,
		Args:      []string{"upload-pack", "--advertise-refs", repo},
		Env:       proc.Bare(),
		Timeout:   time.Minute,
		MaxOutput: 1 << 20,
		Drain:     2 * time.Second,
	})
	if err != nil || res.Code != 0 {
		return 98
	}
	_, _ = os.Stdout.Write(res.Stdout)
	return 0
}
