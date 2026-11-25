package names

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxRun is the most words a name of the list holds, and the longest run of
// words the scan hashes.
const MaxRun = 4

// maxLetters is the largest letter count a list line may carry.
const maxLetters = 1000

// ErrEmpty is a scan given nothing to read: it fails closed, since a scan of
// nothing proves nothing.
var ErrEmpty = errors.New("nothing to scan")

// row is what one list line says past its hash: the letter count, 0 when it
// carries none, and the what, "" when it carries none.
type row struct {
	letters int
	what    string
}

// List is the forbidden-names list: the SHA-256 of each name, folded to
// lower case, with the rows that carry it.
type List struct {
	rows map[[sha256.Size]byte][]row
	// counts are the letter counts the rows carry, each once, in order.
	counts []int
	// lines is how many lines held a hash.
	lines int
	// windows is what the letter-count rule found in each word it has met, by
	// the word folded: a tree repeats its words, so each is hashed once.
	windows map[string][]row
}

// ReadList reads the list: a line is a SHA-256 in hex, 64 digits, optionally
// followed by a letter count and then a what, the rest of the line. A blank
// line and one that starts with # are passed over. A count of 0 is no count,
// so a row with a what and no count can be written. Any other line is
// refused by its number alone, since it may hold a name in the clear, and a
// list of no hash is ErrEmpty.
func ReadList(data []byte) (*List, error) {
	l := &List{rows: map[[sha256.Size]byte][]row{}, windows: map[string][]row{}}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\xef\xbb\xbf"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, r, ok := parseLine(line)
		if !ok {
			return nil, fmt.Errorf("line %d of the list is not a SHA-256 in hex (64 digits), with an optional letter count and a what after it: make the line with hatch names hash", i+1)
		}
		l.lines++
		if !slices.Contains(l.rows[key], r) {
			l.rows[key] = append(l.rows[key], r)
		}
		if r.letters > 0 && !slices.Contains(l.counts, r.letters) {
			l.counts = append(l.counts, r.letters)
		}
	}
	if l.lines == 0 {
		return nil, fmt.Errorf("%w: the list holds no hash", ErrEmpty)
	}
	slices.Sort(l.counts)
	return l, nil
}

// parseLine reads one list line that is neither blank nor a comment: its
// fields are split by spaces or tabs.
func parseLine(line string) (key [sha256.Size]byte, r row, ok bool) {
	fields := strings.Fields(line)
	b, err := hex.DecodeString(fields[0])
	if err != nil || len(b) != sha256.Size {
		return key, r, false
	}
	if len(fields) > 1 {
		n, err := strconv.Atoi(fields[1])
		if err != nil || n < 0 || n > maxLetters || strings.Trim(fields[1], "0123456789") != "" {
			return key, r, false
		}
		r = row{letters: n, what: strings.Join(fields[2:], " ")}
	}
	return [sha256.Size]byte(b), r, true
}

// Lines is how many lines of the list held a hash.
func (l *List) Lines() int { return l.lines }

// find is the rows that carry the hash of s, nil when the list holds none.
func (l *List) find(s string) []row {
	return l.rows[sha256.Sum256([]byte(s))]
}

// hashOf is the SHA-256 of s in lower-case hex.
func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// HashLines is the list lines of one name as a person writes it: the SHA-256
// of its words, folded and joined by one space, and, when it has more than
// one, of its words run together, so the scan finds the name written either
// way. A word is a run of letters, marks and digits, split where its case
// turns, as the scan splits text. A name whose words are joined by hyphens
// alone also gets the line of the name as written, folded, which the hyphen
// rule reads. With letters, each line whose hash is of one unbroken word
// carries that word's letter count, so the scan also finds it glued inside a
// longer word; the count says how long the name is, which is the caller's to
// weigh. A name of no word, or of more than MaxRun, is refused, and the
// refusal never holds the name.
func HashLines(name string, letters bool) ([]string, error) {
	ps, _ := words(name)
	switch {
	case len(ps) == 0:
		return nil, errors.New("a name holds no word")
	case len(ps) > MaxRun:
		return nil, fmt.Errorf("a name of %d words: the scan hashes runs of 1 to %d", len(ps), MaxRun)
	}
	folded := make([]string, len(ps))
	for i, p := range ps {
		folded[i] = p.word
	}
	counted := func(s string) string {
		if !letters {
			return hashOf(s)
		}
		return hashOf(s) + " " + strconv.Itoa(utf8.RuneCountInString(s))
	}
	if len(ps) == 1 {
		return []string{counted(folded[0])}, nil
	}
	out := []string{hashOf(strings.Join(folded, " ")), counted(strings.Join(folded, ""))}
	if whole := strings.ToLower(strings.TrimSpace(name)); hyphenated(whole) {
		out = append(out, hashOf(whole))
	}
	return out, nil
}

// hyphenated is whether s is words joined by single hyphens and nothing
// else, the shape the hyphen rule hashes.
func hyphenated(s string) bool {
	runs := wordRun.FindAllStringIndex(s, -1)
	if len(runs) < 2 || runs[0][0] != 0 || runs[len(runs)-1][1] != len(s) {
		return false
	}
	for i := 1; i < len(runs); i++ {
		if s[runs[i-1][1]:runs[i][0]] != "-" {
			return false
		}
	}
	return true
}
