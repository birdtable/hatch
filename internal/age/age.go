// Package age refuses a Go module version younger than the caller's window.
//
// Built on: the standard library, go-proc's proc.Run, which starts the go command, and golang.org/x/mod/module, which tells a pseudo-version; the
// release time of each module is the go command's own answer to `go list -m -json all`, from
// the module proxy or the cache, so hatch holds no registry client.
// Adds: the comparison of each Time with now and with the window, the refusal of a
// pseudo-version (its time is the committer's own, and can be any date), of a module with no
// Time (a replace to a folder) and of a run that could not read the modules, and the
// caller's exceptions, each with its reason, reported when used and when not.
// Configured by: the caller. The window, the modfiles and the exceptions file are the caller's
// flags; hatch holds no number, no module and no exception of its own.
package age

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/birdtable/go-proc/proc"
	"golang.org/x/mod/module"
)

const (
	exitGreen  = 0
	exitRed    = 1
	exitUsage  = 2
	exitNoGo   = 3
	maxOutput  = 64 << 20
	drain      = 10 * time.Second
	defaultRun = 5 * time.Minute
)

const usage = `usage: hatch age --days <n> --modfile <go.mod path> [--modfile <path> ...] [--exceptions <file>] [--timeout <duration>]

--days        the days a module version must be old before it is used; required, more than zero, and no
              default: the caller's rule gives it
--modfile     a module file to check, such as go.mod or tools/task.mod; required, and given once for each file
--exceptions  a file of the caller's exceptions, one line each: <module>@<version> <reason>. Blank lines and lines
              starting with # are skipped; a line with no reason is refused. An exception matches one module and
              one version, the one of the require line; unset, there are none
--timeout     how long each go list may take (default 5m)

For each modfile it runs go list -m -json -modfile=<file> all from the current folder and reads each module's
release time from the go command's own answer. A module version is refused, with its line on stdout, when it is
younger than --days (the line names its release time and the UTC day it is allowed from), when it is a
pseudo-version (its time is its commit's own and can be any date), or when it has no time, as a module replaced
by a folder has none (the line names its module@version, which is the key an exception takes). A module replaced
by another module is judged by the replacement's version and time. A module excepted is allowed, and says so with
its reason. An exception that matches no module that is refused is printed, and does not fail the run. The main module is not judged.

exit codes of hatch age:
  0  green: no module is refused
  1  red: a module is refused, each on a line of its own
  2  a usage error, an exceptions file that cannot be read, or a go list that failed (a cold cache with no
     network, a bad modfile): the reason is on stderr, and the run is never green
  3  go not found on PATH
`

// listed is the part of a module go list -m -json prints that age reads.
type listed struct {
	Path    string
	Version string
	Time    string
	Main    bool
	Replace *struct {
		Path    string
		Version string
		Time    string
	}
}

// exception is one line of the exceptions file.
type exception struct {
	key, reason string
	used        bool
}

// modfiles is the repeated --modfile flag.
type modfiles []string

func (m *modfiles) String() string     { return strings.Join(*m, ",") }
func (m *modfiles) Set(v string) error { *m = append(*m, v); return nil }

// Run runs hatch age and returns its exit code. now is the time a module's age is counted to.
func Run(ctx context.Context, args []string, now time.Time, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("age", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var days int
	var files modfiles
	var exceptionsFile string
	timeout := defaultRun
	fl.IntVar(&days, "days", 0, "")
	fl.Var(&files, "modfile", "")
	fl.StringVar(&exceptionsFile, "exceptions", "", "")
	fl.DurationVar(&timeout, "timeout", timeout, "")
	if err := fl.Parse(args); err != nil || fl.NArg() > 0 || days <= 0 || len(files) == 0 || timeout <= 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	var exceptions []*exception
	if exceptionsFile != "" {
		var err error
		if exceptions, err = readExceptions(exceptionsFile); err != nil {
			fmt.Fprintf(stderr, "hatch age: %v\n", err)
			return exitUsage
		}
	}
	goPath, err := exec.LookPath("go")
	if errors.Is(err, exec.ErrNotFound) {
		fmt.Fprintln(stderr, "hatch age: go not found on PATH")
		return exitNoGo
	}
	if err == nil {
		goPath, err = filepath.Abs(goPath)
	}
	if err != nil {
		fmt.Fprintln(stderr, "hatch age:", err)
		return exitUsage
	}
	window := time.Duration(days) * 24 * time.Hour
	checked, refused := 0, 0
	for _, f := range files {
		mods, err := list(ctx, goPath, f, timeout)
		if err != nil {
			fmt.Fprintf(stderr, "hatch age: %s: %v\n", f, err)
			return exitUsage
		}
		for _, m := range mods {
			if m.Main {
				continue
			}
			checked++
			if why := judge(m, now, window); why != "" {
				if e := find(exceptions, m.Path+"@"+m.Version); e != nil {
					e.used = true
					fmt.Fprintf(stdout, "hatch age: %s@%s excepted: %s\n", m.Path, m.Version, e.reason)
					continue
				}
				refused++
				fmt.Fprintf(stdout, "hatch age: %s: %s@%s %s\n", f, m.Path, m.Version, why)
			}
		}
	}
	for _, e := range exceptions {
		if !e.used {
			fmt.Fprintf(stdout, "hatch age: exception %s matches no module that is refused\n", e.key)
		}
	}
	if refused > 0 {
		fmt.Fprintf(stdout, "hatch age: %d of %d modules refused\n", refused, checked)
		return exitRed
	}
	fmt.Fprintf(stdout, "hatch age: %d modules checked, none refused\n", checked)
	return exitGreen
}

// judge is why the module is refused, or "" when it is allowed: the words that follow its module@version.
func judge(m listed, now time.Time, window time.Duration) string {
	version, stamp := m.Version, m.Time
	if m.Replace != nil {
		if m.Replace.Version == "" {
			return "is replaced by a folder, which has no release time"
		}
		version, stamp = m.Replace.Version, m.Replace.Time
	}
	if module.IsPseudoVersion(version) {
		return fmt.Sprintf("is a pseudo-version (%s), whose time is its commit's own and can be any date", version)
	}
	if stamp == "" {
		return "has no release time"
	}
	released, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return fmt.Sprintf("has a release time that cannot be read (%q)", stamp)
	}
	if age := now.Sub(released); age < window {
		return fmt.Sprintf("is %d days old, under the %d days: released %s, allowed from %s",
			int(age/(24*time.Hour)), int(window/(24*time.Hour)),
			released.UTC().Format(time.RFC3339), released.Add(window).UTC().Format("2006-01-02"))
	}
	return ""
}

// find is the exception of key, or nil.
func find(exceptions []*exception, key string) *exception {
	i := slices.IndexFunc(exceptions, func(e *exception) bool { return e.key == key })
	if i < 0 {
		return nil
	}
	return exceptions[i]
}

// readExceptions reads the exceptions file: <module>@<version> <reason> on a line, blank lines and # lines
// skipped. A line with no reason, or a key given twice, is refused.
func readExceptions(path string) ([]*exception, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the exceptions file cannot be read: %w", err)
	}
	var out []*exception
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, reason, _ := strings.Cut(line, " ")
		reason = strings.TrimSpace(reason)
		switch {
		case !strings.Contains(key, "@") || strings.HasSuffix(key, "@") || strings.HasPrefix(key, "@"):
			return nil, fmt.Errorf("%s:%d: %q is not <module>@<version>", path, n, key)
		case reason == "":
			return nil, fmt.Errorf("%s:%d: %s has no reason", path, n, key)
		case find(out, key) != nil:
			return nil, fmt.Errorf("%s:%d: %s is excepted twice", path, n, key)
		}
		out = append(out, &exception{key: key, reason: reason})
	}
	return out, sc.Err()
}

// list is the modules of a modfile as the go command lists them. A failure of the command, or output that is not
// the modules, is an error: the run is never green on what it could not read.
func list(ctx context.Context, goPath, modfile string, timeout time.Duration) ([]listed, error) {
	res, err := proc.Run(ctx, proc.Cmd{
		Path:    goPath,
		Args:    []string{"list", "-m", "-json", "-modfile=" + modfile, "all"},
		Env:     os.Environ(),
		Timeout: timeout,
		// No Grace: a go list that reaches its timeout is killed, tree and all, on every system (a Grace above
		// zero is refused on Windows, and no system name is read here).
		Drain:     drain,
		MaxOutput: maxOutput,
	})
	if err != nil {
		return nil, fmt.Errorf("go list did not run to its end: %w", err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("go list exited %d: %s", res.Code, strings.TrimSpace(string(res.Stderr)))
	}
	var mods []listed
	dec := json.NewDecoder(bytes.NewReader(res.Stdout))
	for {
		var m listed
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list printed what is not a module: %w", err)
		}
		mods = append(mods, m)
	}
	if len(mods) == 0 {
		return nil, errors.New("go list printed no module")
	}
	return mods, nil
}
