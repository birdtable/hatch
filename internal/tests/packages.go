package tests

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

const packagesUsage = `usage: hatch tests packages [exclude...]
  Prints the import paths of go list ./..., one a line, without the paths named.
  An exclude is an exact import path, or a path ending in /... which drops that
  path and everything under it. The list is the caller's: no path is dropped by
  default. It exits 1 when nothing is left, 2 on a usage or run error and 3 when
  go is not on PATH.
`

// Exclude returns the paths that no exclude names. An exclude is an exact import path, or a path ending in /...,
// which names that path and every path under it.
func Exclude(paths, excludes []string) []string {
	kept := make([]string, 0, len(paths))
next:
	for _, p := range paths {
		for _, x := range excludes {
			if prefix, ok := strings.CutSuffix(x, "/..."); ok {
				if p == prefix || strings.HasPrefix(p, prefix+"/") {
					continue next
				}
			} else if p == x {
				continue next
			}
		}
		kept = append(kept, p)
	}
	return kept
}

// Packages runs go list ./... in the working folder and prints the import paths it lists, one a line, without
// those the excludes name. It returns the exit code: 0 with at least one path printed, 1 when none is left, 2 when
// an exclude is blank or go list failed, ExitNoGo when go is not on PATH.
func Packages(ctx context.Context, excludes []string, stdout, stderr io.Writer) int {
	for _, x := range excludes {
		if strings.TrimSpace(x) == "" || strings.HasPrefix(x, "-") {
			fmt.Fprintf(stderr, "hatch tests packages: %q is not an import path\n", x)
			io.WriteString(stderr, packagesUsage)
			return 2
		}
	}
	goPath, err := exec.LookPath("go")
	if errors.Is(err, exec.ErrNotFound) {
		fmt.Fprintln(stderr, "hatch tests packages: go not found on PATH")
		return ExitNoGo
	}
	if err != nil {
		fmt.Fprintln(stderr, "hatch tests packages:", err)
		return 2
	}
	var listed bytes.Buffer
	cmd := exec.CommandContext(ctx, goPath, "list", "./...")
	cmd.Stdout, cmd.Stderr = &listed, stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(stderr, "hatch tests packages: go list ./... failed:", err)
		return 2
	}
	kept := Exclude(strings.Fields(listed.String()), excludes)
	if len(kept) == 0 {
		fmt.Fprintln(stderr, "hatch tests packages: no package is left")
		return 1
	}
	for _, p := range kept {
		fmt.Fprintln(stdout, p)
	}
	return 0
}
