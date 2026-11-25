package tests

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExclude(t *testing.T) {
	all := []string{"m", "m/cmd/a", "m/internal/ui", "m/internal/uitest", "m/internal/ui/golden"}
	rows := []struct {
		name     string
		excludes []string
		want     []string
	}{
		{"none named, none dropped", nil, all},
		{"an exact path drops only itself", []string{"m/internal/ui"}, []string{"m", "m/cmd/a", "m/internal/uitest", "m/internal/ui/golden"}},
		{"a /... prefix drops the path and everything under it", []string{"m/internal/ui/..."}, []string{"m", "m/cmd/a", "m/internal/uitest"}},
		{"a /... prefix is a path boundary, not a string prefix", []string{"m/internal/u/..."}, all},
		{"the module root with /... drops all", []string{"m/..."}, []string{}},
		{"two excludes", []string{"m", "m/cmd/..."}, []string{"m/internal/ui", "m/internal/uitest", "m/internal/ui/golden"}},
		{"a path no package has drops nothing", []string{"m/nowhere"}, all},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if got := Exclude(all, r.excludes); !slices.Equal(got, r.want) {
				t.Fatalf("got %v, want %v", got, r.want)
			}
		})
	}
}

// packagesModule makes a module of three packages, a, b and b/c, and works in it.
func packagesModule(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for name, text := range map[string]string{
		"go.mod":     "module example.test/m\n\ngo 1.24\n",
		"a/a.go":     "package a\n",
		"b/b.go":     "package b\n",
		"b/c/c.go":   "package c\n",
		"doc.go":     "package m\n",
		"b/c/c2.txt": "not go\n",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
}

func packages(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(context.Background(), append([]string{"packages"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestPackagesListsWhatGoListSaysWithoutTheExcludes(t *testing.T) {
	packagesModule(t)
	code, out, _ := packages("example.test/m/b/...")
	if code != 0 || out != "example.test/m\nexample.test/m/a\n" {
		t.Fatalf("code %d, out %q", code, out)
	}
	code, out, _ = packages()
	if code != 0 || strings.Count(out, "\n") != 4 {
		t.Fatalf("no excludes: code %d, out %q", code, out)
	}
}

func TestPackagesExitsOneWhenNothingIsLeft(t *testing.T) {
	packagesModule(t)
	code, out, errs := packages("example.test/m/...")
	if code != 1 || out != "" || !strings.Contains(errs, "no package is left") {
		t.Fatalf("code %d, out %q, err %q", code, out, errs)
	}
}

func TestPackagesRefusesABlankOrFlagLikeExclude(t *testing.T) {
	packagesModule(t)
	for _, x := range []string{"", " ", "-race"} {
		code, out, errs := packages(x)
		if code != 2 || out != "" || !strings.Contains(errs, "is not an import path") {
			t.Fatalf("%q: code %d, out %q, err %q", x, code, out, errs)
		}
	}
}

func TestPackagesExitsTwoWhenGoListFails(t *testing.T) {
	t.Chdir(t.TempDir())
	code, out, errs := packages()
	if code != 2 || out != "" || !strings.Contains(errs, "go list ./... failed") {
		t.Fatalf("code %d, out %q, err %q", code, out, errs)
	}
}
