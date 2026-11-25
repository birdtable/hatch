package names

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// kind is which surface a finding is on.
type kind int

const (
	kindContent kind = iota
	kindPath
	kindBranch
	kindMessage
	kindAdded
	kindAddedPath
	kindAddress
	kindStdin
	kindRewrite
	kindUnused
)

// finding is one thing a scan found. text is all that is printed of it: it
// holds <name> in place of a name, a file's path with each name in it masked,
// and a line's number, and never the name, the text around it, an address or
// a row's what. file is the path as it is, for the proof, and is not printed.
type finding struct {
	kind kind
	file string
	text string
}

// commit is one commit past the base.
type commit struct {
	// id is its hash; author and committer are their addresses.
	id, author, committer string
	// message is its whole message.
	message string
	// patch is what it changes, as git log -p prints it with patchArgs: a
	// name it adds and a later commit removes is published all the same, and
	// is read here.
	patch string
	// written is every path it adds or changes, as git prints them with
	// writtenArgs: the patch holds none for a file with no line.
	written []string
}

// taken is what a scan read of each surface it was asked for.
type taken struct {
	// tree is whether the tree was read; paths is every path git tracks, in
	// git's order, and files the body of each that is a file on disk.
	tree  bool
	paths []string
	files map[string][]byte
	// missing is each tracked path that is no longer on disk: git holds a
	// body the scan cannot read, so the surface is unread.
	missing []string
	// branch is the branch HEAD is on, "" when the surface was not asked for.
	branch string
	// commits are the commits past the base, newest first: none when
	// rewrites holds a finding, as the history git would show is not the one
	// a push sends.
	commits  []commit
	rewrites []string
	// stdin is the text given on stdin, "" when the surface was not asked for.
	stdin string
}

// judge is the findings in what a scan read: each rewrite of the history;
// each name of the list in a tracked file, its path, the branch, a commit's
// message, a line or a path a commit adds, and the text on stdin; each
// address that addresses does not match whole, when it is given; and, when
// the tree was read, each exception nothing used. An exception holds for a
// file's body and for the lines a commit adds to that file, never for a path.
func judge(l *List, ex *Exceptions, t taken, addresses *regexp.Regexp) []finding {
	var out []finding
	seen := map[string]bool{}
	add := func(k kind, file, text string) {
		if !seen[text] {
			seen[text] = true
			out = append(out, finding{k, file, text})
		}
	}
	for _, r := range t.rewrites {
		add(kindRewrite, "", r)
	}
	for _, p := range t.paths {
		inPath := l.hits(p)
		shown := masked(p, inPath)
		if len(inPath) > 0 {
			add(kindPath, p, shown+": "+mask+" in its path")
		}
		body, ok := t.files[p]
		if !ok {
			continue
		}
		text := string(body)
		for _, h := range l.hits(text) {
			if !ex.excepts(p, text, h) {
				add(kindContent, p, fmt.Sprintf("%s:%d: %s", shown, lineOf(text, h.start), mask))
			}
		}
	}
	if len(l.hits(t.branch)) > 0 {
		add(kindBranch, "", "branch: "+mask)
	}
	for _, c := range t.commits {
		judgeCommit(l, ex, c, addresses, add)
	}
	for _, h := range l.hits(t.stdin) {
		add(kindStdin, "", fmt.Sprintf("stdin:%d: %s", lineOf(t.stdin, h.start), mask))
	}
	if t.tree {
		for _, line := range ex.unused() {
			add(kindUnused, "", fmt.Sprintf("exceptions:%d: nothing in the tree used this exception: delete it", line))
		}
	}
	return out
}

// judgeCommit gives add the findings in one commit: its message, the paths
// it writes, the lines it adds, read across their line breaks with the line
// of the file each lies on, and its two addresses.
func judgeCommit(l *List, ex *Exceptions, c commit, addresses *regexp.Regexp, add func(kind, string, string)) {
	id := "commit " + c.id[:min(len(c.id), 12)] + ": "
	for _, h := range l.hits(c.message) {
		add(kindMessage, "", fmt.Sprintf("%smessage:%d: %s", id, lineOf(c.message, h.start), mask))
	}
	paths, runs := parsePatch(c.patch)
	for _, p := range c.written {
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	for _, p := range paths {
		if hs := l.hits(p); len(hs) > 0 {
			add(kindAddedPath, p, id+masked(p, hs)+": "+mask+" in the path it writes")
		}
	}
	for _, r := range runs {
		shown := masked(r.path, l.hits(r.path))
		for _, h := range l.hits(r.text) {
			if !ex.excepts(r.path, r.text, h) {
				add(kindAdded, r.path, fmt.Sprintf("%s%s:%d: %s in a line it adds", id, shown, r.line+lineOf(r.text, h.start)-1, mask))
			}
		}
	}
	if addresses == nil {
		return
	}
	if !addresses.MatchString(c.author) {
		add(kindAddress, "", id+"its author address does not match --addresses")
	}
	if !addresses.MatchString(c.committer) {
		add(kindAddress, "", id+"its committer address does not match --addresses")
	}
}

// whole is the caller's pattern made to match a whole address, so a pattern
// that matches a part of one does not pass it.
func whole(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(`^(?:` + pattern + `)$`)
}

// has is whether fs holds a finding of kind k, on file when file is not "".
func has(fs []finding, k kind, file string) bool {
	for _, f := range fs {
		if f.kind == k && (file == "" || f.file == file) {
			return true
		}
	}
	return false
}

// lines is the findings' texts, one a line.
func lines(fs []finding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.text + "\n")
	}
	return b.String()
}
