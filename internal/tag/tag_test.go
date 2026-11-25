package tag

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/birdtable/hatch/internal/gitrun"
)

// gitT runs git in dir as a person who commits unsigned, with no configuration of the machine's, and fails the
// test when it fails.
func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	p, err := gitrun.Find()
	if err != nil {
		t.Fatal("git is not on PATH")
	}
	g := gitrun.Git{Path: p, Timeout: time.Minute, MaxOutput: 1 << 20}
	full := append([]string{"-c", "user.name=T", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)
	res, err := g.Run(context.Background(), dir, full...)
	if err != nil || res.Code != 0 {
		t.Fatalf("git %s failed: %v\n%s", args[0], err, res.Stderr)
	}
}

// repoFor is a clone, on branch, of a bare remote that holds its one commit, and a second clone of the same
// remote, for a push the first has not pulled.
func repoFor(t *testing.T, branch string) (work, other string) {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	work, other = filepath.Join(dir, "work"), filepath.Join(dir, "other")
	gitT(t, dir, "init", "-q", "--bare", "-b", branch, remote)
	gitT(t, dir, "init", "-q", "-b", branch, work)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, work, "add", ".")
	gitT(t, work, "commit", "-q", "-m", "base")
	gitT(t, work, "remote", "add", "origin", remote)
	gitT(t, work, "push", "-q", "origin", branch)
	gitT(t, dir, "clone", "-q", remote, other)
	return work, other
}

// commitIn makes one more commit in dir.
func commitIn(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-q", "-m", name)
}

// guardOn runs hatch tag on the repository dir, in process, and answers what it said on both streams, the stream
// of a refusal last, and its exit code.
func guardOn(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	var out, errs bytes.Buffer
	code := Run(context.Background(), append([]string{"--dir", dir}, args...), &out, &errs)
	return out.String(), errs.String(), code
}

// A plant is what a row does to the clone and the other clone before the guard looks.
type plant func(t *testing.T, work, other string)

func write(name, text string) plant {
	return func(t *testing.T, work, _ string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func pushedAndFetched(t *testing.T, work, other string) {
	t.Helper()
	commitIn(t, other, "b.txt")
	gitT(t, other, "push", "-q", "origin", "main")
	gitT(t, work, "fetch", "-q", "origin")
}

// named is annotated for the tag name, as a prefixed tag is: mod/v0.1.0.
func named(name string, push bool) plant {
	return func(t *testing.T, work, _ string) {
		t.Helper()
		gitT(t, work, "tag", "-a", "-m", name, name)
		if push {
			gitT(t, work, "push", "-q", "origin", name)
		}
	}
}

// onSide leaves the clone on a branch the remote holds, one commit past main, with the prefixed tag on it, pushed.
func onSide(t *testing.T, work, _ string) {
	t.Helper()
	gitT(t, work, "checkout", "-q", "-b", "side")
	commitIn(t, work, "s.txt")
	gitT(t, work, "push", "-q", "origin", "side")
	named("mod/v0.1.0", true)(t, work, "")
}

func annotated(push bool) plant {
	return func(t *testing.T, work, _ string) {
		t.Helper()
		gitT(t, work, "tag", "-a", "-m", "v0.1.0", "v0.1.0")
		if push {
			gitT(t, work, "push", "-q", "origin", "v0.1.0")
		}
	}
}

// Each moment of a release holds the tree to what it asks, and a refusal names its reason on stderr and exits 1:
// before the tag, a clean main that is the remote's passes, and a dirty tree, a branch behind or ahead of the
// remote or apart from it, a branch that is not the caller's, a tag that exists here or on the remote and no
// version are refused; --snapshot passes a detached HEAD and refuses a dirty tree and a branch behind its remote's;
// --tagged refuses a lightweight tag, a tag that is not HEAD and a tag the remote lacks, and a decoy ref whose name
// ends as the tag's does not answer for it, and passes an annotated tag pushed.
//
// What it costs when this fails: a release is built from a commit that is not the one its tag names, or a
// refusal does not say why.
func TestTheTagGuardRefusesEachPlantByName(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		plant plant
		args  []string
		code  int
		want  string
	}{
		{"control", nil, []string{"--remote", "origin", "--branch", "main", "v0.1.0"}, 0, "ok: main at"},
		{"dirty", write("new.txt", "x"), []string{"--remote", "origin", "--branch", "main", "v0.1.0"}, 1, "the tree is not clean, 1 paths"},
		{"behind", pushedAndFetched, []string{"--remote", "origin", "--branch", "main", "v0.1.0"}, 1, "main is behind origin's main"},
		{"behind, not fetched", func(t *testing.T, _, other string) {
			t.Helper()
			commitIn(t, other, "b.txt")
			gitT(t, other, "push", "-q", "origin", "main")
		}, []string{"--remote", "origin", "--branch", "main", "v0.1.0"}, 1, "main is not origin's main"},
		{"ahead", func(t *testing.T, work, _ string) {
			t.Helper()
			commitIn(t, work, "c.txt")
		}, []string{"--remote", "origin", "--branch", "main", "v0.1.0"}, 1, "main is ahead of origin's main"},
		{"not the branch", func(t *testing.T, work, _ string) {
			t.Helper()
			gitT(t, work, "checkout", "-q", "-b", "other")
		}, []string{"--remote", "origin", "--branch", "main", "v0.1.0"}, 1, "HEAD is other, not main"},
		{"tag here", func(t *testing.T, work, _ string) {
			t.Helper()
			gitT(t, work, "tag", "v0.1.0")
		}, []string{"--remote", "origin", "--branch", "main", "v0.1.0"}, 1, "the tag v0.1.0 exists here"},
		{"tag on the remote", func(t *testing.T, _, other string) {
			t.Helper()
			gitT(t, other, "tag", "-a", "-m", "v0.1.0", "v0.1.0")
			gitT(t, other, "push", "-q", "origin", "v0.1.0")
		}, []string{"--remote", "origin", "--branch", "main", "v0.1.0"}, 1, "the tag v0.1.0 exists on origin"},
		{"no version", nil, []string{"--remote", "origin", "--branch", "main", ""}, 1, "no version to release"},
		{"not a version", nil, []string{"--remote", "origin", "--branch", "main", "0.1.0"}, 1, "no version to release"},
		{"no remote", nil, []string{"--remote", "nowhere", "--branch", "main", "v0.1.0"}, 1, "nowhere has no main"},
		{"snapshot detached", func(t *testing.T, work, _ string) {
			t.Helper()
			gitT(t, work, "checkout", "-q", "--detach")
			commitIn(t, work, "d.txt")
		}, []string{"--remote", "origin", "--snapshot"}, 0, "ok: a detached HEAD at"},
		{"snapshot", nil, []string{"--remote", "origin", "--snapshot"}, 0, "ok: main at"},
		{"snapshot dirty", write("a.txt", "edited\n"), []string{"--remote", "origin", "--snapshot"}, 1, "the tree is not clean"},
		{"snapshot behind", pushedAndFetched, []string{"--remote", "origin", "--snapshot"}, 1, "main is behind origin's main"},
		{"tagged lightweight", func(t *testing.T, work, _ string) {
			t.Helper()
			gitT(t, work, "tag", "v0.1.0")
			gitT(t, work, "push", "-q", "origin", "v0.1.0")
		}, []string{"--remote", "origin", "--tagged", "v0.1.0"}, 1, "v0.1.0 is no annotated tag here"},
		{"tagged not HEAD", func(t *testing.T, work, _ string) {
			t.Helper()
			gitT(t, work, "tag", "-a", "-m", "v0.1.0", "v0.1.0")
			commitIn(t, work, "e.txt")
		}, []string{"--remote", "origin", "--tagged", "v0.1.0"}, 1, "v0.1.0 is not HEAD"},
		{"tagged not pushed", annotated(false), []string{"--remote", "origin", "--tagged", "v0.1.0"}, 1, "origin has no tag v0.1.0"},
		{"tagged not pushed, a decoy whose ref ends as the tag's on the remote", func(t *testing.T, work, _ string) {
			t.Helper()
			gitT(t, work, "tag", "-a", "-m", "v0.1.0", "v0.1.0")
			gitT(t, work, "push", "-q", "origin", "v0.1.0:refs/tags/x/refs/tags/v0.1.0")
		}, []string{"--remote", "origin", "--tagged", "v0.1.0"}, 1, "origin has no tag v0.1.0"},
		{"tagged, the remote's tag is another commit than HEAD", func(t *testing.T, work, other string) {
			t.Helper()
			commitIn(t, other, "f.txt")
			gitT(t, other, "tag", "-a", "-m", "v0.1.0", "v0.1.0")
			gitT(t, other, "push", "-q", "origin", "v0.1.0")
			gitT(t, work, "tag", "-a", "-m", "v0.1.0", "v0.1.0")
		}, []string{"--remote", "origin", "--tagged", "v0.1.0"}, 1, "origin's v0.1.0 is "},
		{"before the tag, a remote that serves the branch and fails the tags read", func(t *testing.T, work, _ string) {
			t.Helper()
			// The remote's uploadpack is this test binary (flaky_test.go): it answers the first read, the
			// branch's, and fails the next, the tags', with git's 128, which is not the 2 of no match.
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			counter := filepath.Join(t.TempDir(), "served")
			gitT(t, work, "config", "protocol.version", "0")
			// Git runs the command through sh: forward slashes and quotes, so a Windows path reaches the binary whole.
			gitT(t, work, "config", "remote.origin.uploadpack", "'"+filepath.ToSlash(exe)+"' flaky-upload-pack '"+filepath.ToSlash(counter)+"'")
		}, []string{"--remote", "origin", "--branch", "main", "v0.1.0"}, 1, "origin's tags could not be read"},
		{"tagged", annotated(true), []string{"--remote", "origin", "--tagged", "v0.1.0"}, 0, "ok: v0.1.0 at"},
		{"tagged, a prefixed tag", named("mod/sub/v0.1.0", true), []string{"--remote", "origin", "--tagged", "--prefix", "mod/sub", "v0.1.0"}, 0, "ok: mod/sub/v0.1.0 at"},
		{"tagged, a prefixed tag given whole", named("mod/v0.1.0", true), []string{"--remote", "origin", "--tagged", "--prefix", "mod", "mod/v0.1.0"}, 0, "ok: mod/v0.1.0 at"},
		{"tagged, the tag has no prefix and one is asked", annotated(true), []string{"--remote", "origin", "--tagged", "--prefix", "mod", "v0.1.0"}, 1, "no annotated tag here"},
		{"before the tag, a prefixed tag that passes", nil, []string{"--remote", "origin", "--branch", "main", "--prefix", "mod", "v0.1.0"}, 0, "ok: main at"},
		{"before the tag, the prefixed tag exists here", named("mod/v0.1.0", false), []string{"--remote", "origin", "--branch", "main", "--prefix", "mod", "v0.1.0"}, 1, "the tag mod/v0.1.0 exists here"},
		{"tagged, a prefixed tag that is lightweight", func(t *testing.T, work, _ string) {
			t.Helper()
			gitT(t, work, "tag", "mod/v0.1.0")
			gitT(t, work, "push", "-q", "origin", "mod/v0.1.0")
		}, []string{"--remote", "origin", "--tagged", "--prefix", "mod", "v0.1.0"}, 1, "mod/v0.1.0 is no annotated tag here"},
		{"tagged, origin holds another object under the prefixed name", func(t *testing.T, work, other string) {
			t.Helper()
			commitIn(t, other, "f.txt")
			named("mod/v0.1.0", true)(t, other, "")
			named("mod/v0.1.0", false)(t, work, "")
		}, []string{"--remote", "origin", "--tagged", "--prefix", "mod", "v0.1.0"}, 1, "origin's mod/v0.1.0 is "},
		{"tagged, a commit on a side branch", onSide, []string{"--remote", "origin", "--tagged", "--prefix", "mod", "v0.1.0"}, 1, "not on origin's main"},
		{"tagged, a side branch named by --on", onSide, []string{"--remote", "origin", "--tagged", "--prefix", "mod", "--on", "side", "v0.1.0"}, 0, "on origin's side"},
		{"tagged, --on names a branch the remote lacks", annotated(true), []string{"--remote", "origin", "--tagged", "--on", "nowhere", "v0.1.0"}, 1, "origin has no branch nowhere"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			work, other := repoFor(t, "main")
			if c.plant != nil {
				c.plant(t, work, other)
			}
			out, errs, code := guardOn(t, work, c.args...)
			if code != c.code || !strings.Contains(out+errs, c.want) {
				t.Errorf("exit %d, want %d, and %q, which should hold %q", code, c.code, out+errs, c.want)
			}
			if c.code == 1 && (out != "" || !strings.HasPrefix(errs, "hatch tag: refused: ") || strings.Count(errs, "\n") != 1) {
				t.Errorf("a refusal is one line on stderr that says so, and nothing on stdout: out %q, err %q", out, errs)
			}
			if c.code == 0 && (errs != "" || strings.Count(out, "\n") != 1) {
				t.Errorf("a pass is one line on stdout: out %q, err %q", out, errs)
			}
		})
	}
}

// A prefix is folder names of lower case, digits, - . and _ joined by /, and anything else is refused before git is
// asked: capitals, a .., an empty element, a slash at either end, a space, an option of git's. --prefix takes no
// --snapshot and --on takes no other moment.
//
// What it costs when this fails: a tag is made with a name no Go module path reads, or git reads a word of the
// caller's as an option.
func TestAPrefixThatIsNoFolderPathIsRefusedBeforeGitIsAsked(t *testing.T) {
	t.Parallel()
	nowhere := filepath.Join(t.TempDir(), "no-repo")
	for _, p := range []string{"Mod", "a/../b", "..", "/a", "a/", "a//b", "a b", "-x", "a/.", "a/b.", "é", "a\\b"} {
		out, errs, code := guardOn(t, nowhere, "--remote", "origin", "--tagged", "--prefix", p, "v0.1.0")
		if code != 1 || out != "" || !strings.Contains(errs, "is no prefix") {
			t.Errorf("prefix %q: exit %d, out %q, err %q, want the refusal of the prefix", p, code, out, errs)
		}
	}
	for _, p := range []string{"mod", "a/b", "a.b/c-d_e", "x1/v2"} {
		if _, errs, code := guardOn(t, nowhere, "--remote", "origin", "--tagged", "--prefix", p, "v0.1.0"); code == 1 && strings.Contains(errs, "is no prefix") {
			t.Errorf("prefix %q is a folder path and is refused: %q", p, errs)
		}
	}
	for _, args := range [][]string{
		{"--remote", "origin", "--snapshot", "--prefix", "mod"},
		{"--remote", "origin", "--snapshot", "--on", "main"},
		{"--remote", "origin", "--branch", "main", "--on", "main", "v0.1.0"},
		{"--remote", "origin", "--tagged", "--on", "-x", "v0.1.0"},
	} {
		if _, _, code := guardOn(t, nowhere, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

// The branch is the caller's: a repository whose release branch is trunk passes before the tag with --branch
// trunk and is refused with --branch main, and hatch holds no branch of its own.
//
// What it costs when this fails: hatch says main where a project's release branch is another name.
func TestTheBranchIsTheCallers(t *testing.T) {
	t.Parallel()
	work, _ := repoFor(t, "trunk")
	if out, errs, code := guardOn(t, work, "--remote", "origin", "--branch", "trunk", "v0.1.0"); code != 0 || !strings.Contains(out, "ok: trunk at") {
		t.Errorf("--branch trunk: exit %d, out %q, err %q", code, out, errs)
	}
	if out, errs, code := guardOn(t, work, "--remote", "origin", "--branch", "main", "v0.1.0"); code != 1 || !strings.Contains(errs, "HEAD is trunk, not main") {
		t.Errorf("--branch main on trunk: exit %d, out %q, err %q", code, out, errs)
	}
}

// A version that is not a release tag is refused before git is asked: in a folder that is no repository the
// refusal is the version's, not a git failure, and a release tag with a pre-release passes the form.
//
// What it costs when this fails: a word that is no version is passed to git as a ref.
func TestAVersionThatIsNoReleaseTagIsRefusedBeforeGitIsAsked(t *testing.T) {
	t.Parallel()
	nowhere := filepath.Join(t.TempDir(), "no-repo")
	for _, v := range []string{"", "0.1.0", "v1", "v01.2.3", "v1.2.3+meta", "latest", "v1.2.3 "} {
		for _, flags := range [][]string{{"--branch", "main"}, {"--tagged"}} {
			out, errs, code := guardOn(t, nowhere, append([]string{"--remote", "origin"}, append(flags, v)...)...)
			if code != 1 || out != "" || !strings.Contains(errs, "is not a release tag") {
				t.Errorf("version %q %v: exit %d, out %q, err %q, want the refusal of the version", v, flags, code, out, errs)
			}
		}
	}
	for _, v := range []string{"v0.1.0", "v1.20.3-rc.1", "v2.0.0-beta-2"} {
		if !tagForm.MatchString(v) {
			t.Errorf("%q is a release tag and the form refuses it", v)
		}
	}
}

// The flags have no default, and a form the usage does not give is exit 2: no remote, no branch before the tag,
// a branch given to another moment, a version where --snapshot takes none, both moments, an unknown flag, a
// remote that is an option of git.
//
// What it costs when this fails: hatch holds a remote or a branch of its own, or git reads a word of the
// caller's as one of its own options.
func TestTheFlagsHaveNoDefaultAndAFormOfNoUsageIsExit2(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{},
		{"v0.1.0"},
		{"--remote", "origin", "v0.1.0"},
		{"--branch", "main", "v0.1.0"},
		{"--remote", "origin", "--branch", "main"},
		{"--remote", "origin", "--branch", "main", "v0.1.0", "extra"},
		{"--remote", "origin", "--snapshot", "v0.1.0"},
		{"--remote", "origin", "--snapshot", "--branch", "main"},
		{"--remote", "origin", "--tagged"},
		{"--remote", "origin", "--tagged", "--branch", "main", "v0.1.0"},
		{"--remote", "origin", "--snapshot", "--tagged", "v0.1.0"},
		{"--remote", "origin", "--force", "--snapshot"},
		{"--remote", "--upload-pack=x", "--branch", "main", "v0.1.0"},
		{"--remote", "origin", "--branch", "--upload-pack=x", "v0.1.0"},
		{"--remote", "", "--snapshot"},
	} {
		var out, errs bytes.Buffer
		if code := Run(context.Background(), args, &out, &errs); code != 2 || out.Len() != 0 || !strings.Contains(errs.String(), "usage: hatch tag") {
			t.Errorf("%v: exit %d, out %q, err %q, want exit 2 and the usage", args, code, out.String(), errs.String())
		}
	}
}

// The usage names the three moments, so a caller reads what each asks.
//
// What it costs when this fails: a caller cannot tell from the usage what --snapshot and --tagged hold the tree to.
func TestTheUsageNamesTheThreeMoments(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"before the tag", "--snapshot", "--tagged", "annotated tag", "changes no ref"} {
		if !strings.Contains(strings.Join(strings.Fields(usage), " "), s) {
			t.Errorf("the usage lacks %q", s)
		}
	}
}

// --snapshot tells a remote that holds no such branch from one that could not be read: ls-remote --exit-code
// exits 2 for no ref, and a remote that is not there is a refusal, never "none was compared".
//
// What it costs when this fails: a dry release with no network or no sign-in passes having compared nothing.
func TestSnapshotRefusesARemoteThatCannotBeRead(t *testing.T) {
	t.Parallel()
	work, _ := repoFor(t, "main")
	gitT(t, work, "remote", "set-url", "origin", filepath.Join(filepath.Dir(work), "nowhere.git"))
	out, errs, code := guardOn(t, work, "--remote", "origin", "--snapshot")
	if code != 1 || out != "" || !strings.Contains(errs, "origin could not be read") {
		t.Errorf("exit %d, want 1 and \"origin could not be read\"\nstdout: %s\nstderr: %s", code, out, errs)
	}
	// A branch the remote does not hold is still no refusal: that is exit 2 of ls-remote.
	gitT(t, work, "remote", "set-url", "origin", filepath.Join(filepath.Dir(work), "remote.git"))
	gitT(t, work, "checkout", "-q", "-b", "topic")
	if out, errs, code = guardOn(t, work, "--remote", "origin", "--snapshot"); code != 0 || !strings.Contains(out, "holds no such branch") {
		t.Errorf("a branch the remote lacks: exit %d, want 0\nstdout: %s\nstderr: %s", code, out, errs)
	}
}

// The calls that read a remote run with the caller's environment, so its HOME and the git configuration there
// (a credential helper, a url rewrite) reach git: the remote of the clone is an address only the caller's
// configuration can read, and each of the three moments reads it.
//
// What it costs when this fails: a release made from a machine that signs in through its own git configuration
// is refused at every moment, or --snapshot compares nothing.
func TestTheRemoteIsReadWithTheCallersHome(t *testing.T) {
	work, _ := repoFor(t, "main")
	gitT(t, work, "tag", "-a", "-m", "v0.1.0", "v0.1.0")
	gitT(t, work, "push", "-q", "origin", "v0.1.0")
	remote := filepath.ToSlash(filepath.Join(filepath.Dir(work), "remote.git"))
	const standIn = "https://stand-in.invalid/remote.git"
	gitT(t, work, "remote", "set-url", "origin", standIn)
	home := t.TempDir()
	cfg := "[url \"" + remote + "\"]\n\tinsteadOf = " + standIn + "\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	os.Unsetenv("GIT_CONFIG_GLOBAL") //nolint:errcheck,gosec // restored by t.Setenv above
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, args := range [][]string{
		{"--remote", "origin", "--snapshot"},
		{"--remote", "origin", "--tagged", "v0.1.0"},
	} {
		out, errs, code := guardOn(t, work, args...)
		if code != 0 || strings.Contains(out, "holds no such branch") {
			t.Errorf("%v: exit %d, want 0 with the remote read\nstdout: %s\nstderr: %s", args, code, out, errs)
		}
	}
	// Before the tag the version must be new: v0.2.0, with main the remote's.
	if out, errs, code := guardOn(t, work, "--remote", "origin", "--branch", "main", "v0.2.0"); code != 0 {
		t.Errorf("before the tag: exit %d, want 0\nstdout: %s\nstderr: %s", code, out, errs)
	}
}

// --snapshot asks the remote only when HEAD is on a branch: a detached HEAD has no branch to compare, so it passes
// with no origin at all, and the same clone on a branch is refused for a remote that cannot be read. The README and
// the usage say so: "a remote that is asked and cannot be read is a refusal".
//
// What it costs when this fails: a dry release from a detached HEAD is refused for a remote it never needed, or the
// README says --snapshot refuses at every moment and a detached one does not.
func TestADetachedSnapshotAsksNoRemote(t *testing.T) {
	t.Parallel()
	work, _ := repoFor(t, "main")
	gitT(t, work, "remote", "remove", "origin")
	out, errs, code := guardOn(t, work, "--remote", "origin", "--snapshot")
	if code != 1 || !strings.Contains(errs, "origin could not be read") {
		t.Errorf("a branch with no origin: exit %d, want 1 and \"origin could not be read\"\nstdout: %s\nstderr: %s", code, out, errs)
	}
	gitT(t, work, "checkout", "-q", "--detach")
	out, errs, code = guardOn(t, work, "--remote", "origin", "--snapshot")
	if code != 0 || !strings.Contains(out, "ok: a detached HEAD at") || errs != "" {
		t.Errorf("a detached HEAD with no origin: exit %d, want 0 and \"ok: a detached HEAD at\"\nstdout: %s\nstderr: %s", code, out, errs)
	}
}
