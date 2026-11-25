package budget

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/birdtable/go-proc/proc"
)

// maxOutput caps what a tier prints through hatch, as the gates do.
const maxOutput = 1 << 28

// ErrTreeNotRead is a tier whose CPU time cannot be said to be the whole tree's: it never passes. Its
// words follow it, and name what was not read.
var ErrTreeNotRead = errors.New("tree not read")

func notRead(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrTreeNotRead, fmt.Sprintf(format, a...))
}

// meter reads the CPU time, user and system, that the processes this one starts from now on have
// used. It is go-proc's reading of each system's tree: getrusage's sum of the children this process
// has waited for on Unix, where a grandchild counts when its own parent waited for it, and the job
// object this process joins on Windows, which holds every descendant. A system or a case where the
// tree cannot be read is an ErrTreeNotRead, by name.
type meter func() (time.Duration, error)

// newMeterFor is newMeter; a row replaces it to make the reading fail.
var newMeterFor = newMeter

// newMeter takes the reading's base and returns the meter that gives the CPU used since.
func newMeter() (meter, error) {
	base, err := proc.ChildrenCPU()
	if err == nil {
		return func() (time.Duration, error) {
			now, err := proc.ChildrenCPU()
			if err != nil {
				return 0, notRead("the children's CPU time cannot be read: %v", err)
			}
			return now - base, nil
		}, nil
	}
	if !errors.Is(err, errors.ErrUnsupported) {
		return nil, notRead("the children's CPU time cannot be read: %v", err)
	}
	// No sum of the waited-for children (Windows): the job object is the tree.
	id, err := proc.JoinTree()
	if err != nil {
		return nil, notRead("no job object to hold the tier: %v", err)
	}
	start, err := proc.Usage(id)
	if err != nil {
		return nil, notRead("the job object's CPU time cannot be read: %v", err)
	}
	return func() (time.Duration, error) {
		u, err := proc.Usage(id)
		if err != nil {
			return 0, notRead("the job object's CPU time cannot be read: %v", err)
		}
		return u.CPU - start.CPU, nil
	}, nil
}

// measure runs the command in a tree of its own and says how much CPU time its tree used, user plus
// system. A descendant still alive when the command's own process has ended is a tree not fully
// reaped, so the sum is short of what the tier used: it is an ErrTreeNotRead and never a reading. The
// command's exit code is returned beside it. The command is killed at timeout, the caller's hang guard.
func measure(ctx context.Context, argv []string, timeout time.Duration, stdout, stderr io.Writer) (code int, used time.Duration, err error) {
	read, err := newMeterFor()
	if err != nil {
		return 0, 0, err
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return 0, 0, fmt.Errorf("%s not found on PATH", argv[0])
	}
	if path, err = filepath.Abs(path); err != nil {
		return 0, 0, err
	}
	res, err := proc.Run(ctx, proc.Cmd{
		Path:      path,
		Args:      argv[1:],
		Env:       os.Environ(), // the tier is the caller's own command: proc's bare environment has no GOCACHE or LocalAppData on Windows
		Timeout:   timeout,
		MaxOutput: maxOutput,
		Drain:     2 * time.Second,
		Stdout:    stdout,
		Stderr:    stderr,
	})
	var ended proc.Ended
	if errors.As(err, &ended) && ended.Why == proc.WhyDeadline {
		return 0, 0, fmt.Errorf("hung: still running at %v, which is %d times its wall limit, and its tree was killed", timeout, hangFactor)
	}
	if err != nil {
		return 0, 0, err
	}
	if res.Left {
		return res.Code, 0, notRead("a descendant outlived the tier")
	}
	used, err = read()
	return res.Code, used, err
}
