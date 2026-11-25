package names

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// exception is one stanza of the exceptions file: in file, a name inside what
// pattern matches on a line, or else a name of the list row whose what this
// is, is no finding, and why says so.
type exception struct {
	file    string
	pattern *regexp.Regexp
	what    string
	why     string
	line    int
	used    bool
}

// Exceptions is the caller's exceptions file, and which of its exceptions a
// scan used.
type Exceptions struct {
	list []*exception
}

// ReadExceptions reads the exceptions file. An exception is a stanza of
// "key: value" lines, ended by a blank line: file, the one tracked file it
// holds for, by its path with / between the parts; exactly one of pattern, a
// regular expression matched on each line of the file, and what, the what of
// a list row; and why, which is required. A line that starts with # is
// passed over. A stanza that cannot be read is refused by its line's number
// alone.
func ReadExceptions(data []byte) (*Exceptions, error) {
	e := &Exceptions{}
	var cur *exception
	end := func() error {
		if cur == nil {
			return nil
		}
		x := cur
		cur = nil
		switch {
		case x.file == "":
			return fmt.Errorf("the exception at line %d of the exceptions file names no file", x.line)
		case x.why == "":
			return fmt.Errorf("the exception at line %d of the exceptions file gives no why", x.line)
		case (x.pattern == nil) == (x.what == ""):
			return fmt.Errorf("the exception at line %d of the exceptions file needs exactly one of pattern and what", x.line)
		}
		e.list = append(e.list, x)
		return nil
	}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\xef\xbb\xbf"))
		if strings.HasPrefix(line, "#") {
			continue
		}
		if line == "" {
			if err := end(); err != nil {
				return nil, err
			}
			continue
		}
		if cur == nil {
			cur = &exception{line: i + 1}
		}
		if err := cur.set(line); err != nil {
			return nil, fmt.Errorf("line %d of the exceptions file: %w", i+1, err)
		}
	}
	if err := end(); err != nil {
		return nil, err
	}
	return e, nil
}

// set reads one "key: value" line into x. Its error never holds the line.
func (x *exception) set(line string) error {
	key, val, ok := strings.Cut(line, ":")
	val = strings.TrimSpace(val)
	if !ok || val == "" {
		return fmt.Errorf("not a key, a colon and a value")
	}
	var into *string
	switch strings.TrimSpace(key) {
	case "file":
		into = &x.file
	case "what":
		into = &x.what
	case "why":
		into = &x.why
	case "pattern":
		if x.pattern != nil {
			return fmt.Errorf("a second pattern in one exception")
		}
		re, err := regexp.Compile(val)
		if err != nil {
			return fmt.Errorf("its pattern is not a regular expression")
		}
		x.pattern = re
		return nil
	default:
		return fmt.Errorf("its key is none of file, pattern, what and why")
	}
	if *into != "" {
		return fmt.Errorf("a key given twice in one exception")
	}
	*into = val
	return nil
}

// excepts is whether an exception holds for the hit h in text, the body of
// file or lines a commit adds to it, and marks each one that does as used. A
// pattern holds when one match of it on h's line covers the whole hit; a
// what, when a row that carries the hit has it.
func (e *Exceptions) excepts(file, text string, h hit) bool {
	if e == nil {
		return false
	}
	held := false
	for _, x := range e.list {
		if x.file != file {
			continue
		}
		if x.pattern == nil {
			if slices.Contains(h.whats, x.what) {
				x.used, held = true, true
			}
			continue
		}
		start := strings.LastIndexByte(text[:h.start], '\n') + 1
		end := len(text)
		if n := strings.IndexByte(text[h.start:], '\n'); n >= 0 {
			end = h.start + n
		}
		if h.end > end {
			continue
		}
		for _, m := range x.pattern.FindAllStringIndex(text[start:end], -1) {
			if start+m[0] <= h.start && h.end <= start+m[1] {
				x.used, held = true, true
			}
		}
	}
	return held
}

// unused is the line of each exception no hit used, in order.
func (e *Exceptions) unused() []int {
	if e == nil {
		return nil
	}
	var lines []int
	for _, x := range e.list {
		if !x.used {
			lines = append(lines, x.line)
		}
	}
	return lines
}
