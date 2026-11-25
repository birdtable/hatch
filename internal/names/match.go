package names

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
)

// span is one word of a text, folded, and the bytes it lies on.
type span struct {
	word       string
	start, end int
}

// The patterns words splits text by: a run of letters, marks and digits;
// within it, a part, upper-case letters and then the rest; and a part's
// upper-case head, and the lower-case letter that may follow it.
var (
	wordRun   = regexp.MustCompile(`[\p{L}\p{M}\p{Nd}]+`)
	casePart  = regexp.MustCompile(`[\p{Lu}\p{Lt}]*[^\p{Lu}\p{Lt}]*`)
	upperHead = regexp.MustCompile(`^[\p{Lu}\p{Lt}]+`)
	lowerNext = regexp.MustCompile(`^\p{Ll}`)
)

// words is text's words, each folded to lower case: a word is a run of
// letters, marks and digits, split where its case turns (parts). Each run is
// also in wholes, folded, as it stands.
func words(text string) (ps, wholes []span) {
	for _, run := range wordRun.FindAllStringIndex(text, -1) {
		ps = append(ps, parts(text, run[0], run[1])...)
		wholes = append(wholes, span{strings.ToLower(text[run[0]:run[1]]), run[0], run[1]})
	}
	return ps, wholes
}

// parts splits the run of letters, marks and digits at text[start:end]
// where its case turns: before an upper-case letter that follows a
// lower-case one or a digit, and before the last of two or more upper-case
// letters that a lower-case one follows, so HTTPServer is HTTP and Server.
func parts(text string, start, end int) []span {
	var out []span
	add := func(s, e int) { out = append(out, span{strings.ToLower(text[s:e]), s, e}) }
	for _, m := range casePart.FindAllStringIndex(text[start:end], -1) {
		s, e := start+m[0], start+m[1]
		if s == e {
			continue
		}
		head := upperHead.FindString(text[s:e])
		if rs := []rune(head); len(rs) > 1 && lowerNext.MatchString(text[s+len(head):e]) {
			at := s + len(head) - len(string(rs[len(rs)-1]))
			add(s, at)
			add(at, e)
			continue
		}
		add(s, e)
	}
	return out
}

// hit is one name of the list in a text: the bytes it lies on, and the what
// of each row that carries it.
type hit struct {
	start, end int
	whats      []string
}

// maxWindows is how many words the letter-count rule remembers before it
// starts over.
const maxWindows = 1 << 16

// hits is every name of the list in text, each place once, in order. It is
// the union of two rules, so a name either finds is found:
//
// Every row is looked for as a run of 1 to MaxRun words, hashed joined by
// one space, and as a whole run of letters, marks and digits whatever its
// case; a run may cross a line break. And as words joined by single hyphens,
// hashed as written: each run of 2 to MaxRun such words, and the whole chain.
//
// A row that carries a letter count is also looked for inside every word: each
// stretch of that many letters, so a name glued in lower case to another word
// is found.
func (l *List) hits(text string) []hit {
	ps, wholes := words(text)
	var out []hit
	add := func(start, end int, rows []row) {
		if len(rows) > 0 {
			out = append(out, hit{start, end, whatsOf(rows)})
		}
	}
	for i := range ps {
		var run strings.Builder
		for k := 0; k < MaxRun && i+k < len(ps); k++ {
			if k > 0 {
				run.WriteByte(' ')
			}
			run.WriteString(ps[i+k].word)
			add(ps[i].start, ps[i+k].end, l.find(run.String()))
		}
	}
	for _, w := range wholes {
		add(w.start, w.end, l.find(w.word))
		add(w.start, w.end, l.inside(w.word))
	}
	l.hyphens(text, wholes, add)
	slices.SortFunc(out, func(a, b hit) int { return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.end, b.end)) })
	var merged []hit
	for _, h := range out {
		if n := len(merged); n > 0 && merged[n-1].start == h.start && merged[n-1].end == h.end {
			for _, w := range h.whats {
				if !slices.Contains(merged[n-1].whats, w) {
					merged[n-1].whats = append(merged[n-1].whats, w)
				}
			}
			continue
		}
		merged = append(merged, h)
	}
	return merged
}

// inside is the rows the letter-count rule finds in word, a folded run of
// letters, marks and digits: each stretch of n letters, for each count n a
// row carries, whose hash the list holds.
func (l *List) inside(word string) []row {
	if len(l.counts) == 0 {
		return nil
	}
	if found, met := l.windows[word]; met {
		return found
	}
	var found []row
	rs := []rune(word)
	for _, n := range l.counts {
		for i := 0; i+n <= len(rs); i++ {
			found = append(found, l.find(string(rs[i:i+n]))...)
		}
	}
	if len(l.windows) >= maxWindows {
		clear(l.windows)
	}
	l.windows[word] = found
	return found
}

// hyphens gives add each run of 2 to MaxRun words of text joined by single
// hyphens, and each whole chain of them, that the list holds as written,
// folded.
func (l *List) hyphens(text string, wholes []span, add func(start, end int, rows []row)) {
	for i := 0; i < len(wholes); {
		j := i
		for j+1 < len(wholes) && text[wholes[j].end:wholes[j+1].start] == "-" {
			j++
		}
		for a := i; a < j; a++ {
			for b := a + 1; b <= j && b-a < MaxRun; b++ {
				add(wholes[a].start, wholes[b].end, l.find(strings.ToLower(text[wholes[a].start:wholes[b].end])))
			}
		}
		if j-i >= MaxRun {
			add(wholes[i].start, wholes[j].end, l.find(strings.ToLower(text[wholes[i].start:wholes[j].end])))
		}
		i = j + 1
	}
}

// whatsOf is the what of each row that carries one, each once.
func whatsOf(rows []row) []string {
	var out []string
	for _, r := range rows {
		if r.what != "" && !slices.Contains(out, r.what) {
			out = append(out, r.what)
		}
	}
	return out
}

// mask is what a finding prints in place of a name.
const mask = "<name>"

// masked is text with each hit written as <name>, so a finding says where a
// name is and never the name.
func masked(text string, hs []hit) string {
	var b strings.Builder
	at := 0
	for _, h := range hs {
		if h.start < at {
			at = max(at, h.end)
			continue
		}
		b.WriteString(text[at:h.start] + mask)
		at = h.end
	}
	b.WriteString(text[at:])
	return b.String()
}

// lineOf is the line, from 1, that the byte at offset lies on.
func lineOf(text string, offset int) int { return strings.Count(text[:offset], "\n") + 1 }
