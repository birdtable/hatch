package runs

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/birdtable/hatch/internal/gitrun"
)

const (
	exitGreen   = 0
	exitRed     = 1
	exitUsage   = 2
	exitNoGit   = 3
	gitTimeout  = 2 * time.Minute
	gitMaxBytes = 256 << 20
	maxFileSize = 8 << 20
)

const usage = `usage: hatch runs --rules <file> [--root <dir>]
       hatch runs list [--root <dir>]

--rules  the rules file (required, no default): a row is a program and the packages that may start it,
         "<program> <package>..."; the row "unseen <package>..." names the packages that may start a
         program chosen where the check cannot see. Blank lines and lines that start with # are passed over.
--root   the folder of the Go module (default .)

It reads the tracked non-test Go files of the module and finds, per package, each program named to
os/exec's Command, CommandContext and LookPath. It does not see a dot-imported os/exec, an exec.Cmd with
Path set, os.StartProcess or syscall.Exec: the lint rows (depguard on os/exec, forbidigo on the other
calls) cover those.

hatch runs list prints "<package> <program>" lines and each unseen site, and judges nothing.

exit codes of hatch runs:
  0  green
  1  a finding
  2  a usage or run error, or a rules file or tree that cannot be read
  3  git not found on PATH
`

// Run runs hatch runs and returns its exit code. It fails closed: anything it
// cannot read is exit 2, never green.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	list := len(args) > 0 && args[0] == "list"
	if list {
		args = args[1:]
	}
	fl := flag.NewFlagSet("runs", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var rulesPath, root string
	fl.StringVar(&root, "root", ".", "")
	if !list {
		fl.StringVar(&rulesPath, "rules", "", "")
	}
	if err := fl.Parse(args); err != nil || fl.NArg() > 0 || (!list && rulesPath == "") {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "hatch runs: %v\n", gitrun.Bare(err))
		if errors.Is(err, gitrun.ErrNoGit) {
			return exitNoGit
		}
		return exitUsage
	}
	var rules *Rules
	if !list {
		data, err := os.ReadFile(rulesPath)
		if err != nil {
			return fail(fmt.Errorf("the rules file cannot be read: %w", err))
		}
		if rules, err = ReadRules(data); err != nil {
			return fail(err)
		}
	}
	files, module, err := readModule(ctx, root)
	if err != nil {
		return fail(err)
	}
	runs, unseen, err := Runs(files, module)
	if err != nil {
		// A file that does not parse is a finding, with its file named.
		name, _, _ := strings.Cut(err.Error(), ": ")
		fmt.Fprintf(stdout, "%s: does not parse\n", name)
		if list {
			return exitUsage
		}
		return exitRed
	}
	w := bufio.NewWriter(stdout)
	defer w.Flush()
	if list {
		for _, pkg := range sortedKeys(runs) {
			for _, prog := range runs[pkg] {
				fmt.Fprintf(w, "%s %s\n", pkg, prog)
			}
		}
		for _, f := range Sorted(unseen) {
			fmt.Fprintf(w, "unseen %s:%d\n", f.File, f.Line)
		}
		return exitGreen
	}
	found := Sorted(rules.Judge(runs, unseen))
	if len(found) == 0 {
		n := 0
		for _, progs := range runs {
			n += len(progs)
		}
		fmt.Fprintf(w, "hatch runs: green, %d programs in %d packages, %d unseen sites\n", n, len(runs), len(unseen))
		return exitGreen
	}
	for _, f := range found {
		switch {
		case f.File == "":
			fmt.Fprintln(w, f.What)
		case f.Line == 0:
			fmt.Fprintf(w, "%s: %s\n", f.File, f.What)
		default:
			fmt.Fprintf(w, "%s:%d: %s\n", f.File, f.Line, f.What)
		}
	}
	return exitRed
}

// readModule is the tracked non-test Go files of the module at root, by path
// relative to it, and the module's path from its go.mod. A folder that holds a
// go.mod of its own is another module, left out.
func readModule(ctx context.Context, root string) (map[string][]byte, string, error) {
	gp, err := gitrun.Find()
	if err != nil {
		return nil, "", err
	}
	g := gitrun.Git{Path: gp, Timeout: gitTimeout, MaxOutput: gitMaxBytes}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, "", err
	}
	// git lists the tracked files under root, relative to it.
	paths, err := g.TrackedPaths(ctx, abs)
	if err != nil {
		return nil, "", err
	}
	nested := map[string]bool{}
	for _, p := range paths {
		if path.Base(p) == "go.mod" && p != "go.mod" {
			nested[path.Dir(p)] = true
		}
	}
	data, ok, err := gitrun.ReadTracked(abs, "go.mod")
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", errors.New("no go.mod is tracked at the root")
	}
	module := modulePath(data)
	if module == "" {
		return nil, "", errors.New("go.mod names no module")
	}
	files := map[string][]byte{}
	for _, rel := range paths {
		if !sourceFile(rel) || inNested(nested, rel) {
			continue
		}
		b, ok, err := gitrun.ReadTracked(abs, rel)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		if len(b) > maxFileSize {
			return nil, "", fmt.Errorf("%s is larger than %d bytes", rel, maxFileSize)
		}
		files[rel] = b
	}
	return files, module, nil
}

func inNested(nested map[string]bool, rel string) bool {
	for d := path.Dir(rel); d != "."; d = path.Dir(d) {
		if nested[d] {
			return true
		}
	}
	return false
}

// modulePath is the path in the module line of a go.mod, or "".
func modulePath(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "module")
		if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		rest, _, _ = strings.Cut(strings.TrimSpace(rest), "//")
		return strings.Trim(strings.TrimSpace(rest), "\"`")
	}
	return ""
}
