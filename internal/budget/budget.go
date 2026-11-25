// Package budget judges the wall time a tier took against its limit, and
// forgives an overrun only when the machine was loaded.
//
// Built on: the standard library, github.com/birdtable/go-proc/load (the
// load average, and the CPU count it was read on) and go-proc/proc (the command --cpu runs, and the CPU
// time of its tree).
// Adds: --cpu, which judges a tier by the CPU time of its tree, user plus system, against a row of the
// caller's file, and names a tree it could not read whole; the load-aware verdict, which reads a limit from the caller's budgets
// file and judges strictly when the load is unknown, as on Windows; the CPU
// count on the load line, as a fact, and --loaded-when before-and-end.
// Configured by: the caller, in every flag of hatch budget; the package holds
// no project policy.
//
// Where the caller samples matters: --load-before is a sample from before the caller's first unit of work
// on the machine, and a sample taken between two tiers of one run holds the earlier tier's load. The end
// sample always holds the judged tier's own load, so --loaded-when end and either excuse a tier by its own
// work and are for a tier that does not load the machine; before and before-and-end, with a sample from
// before the run, tell another job's load from the tier's own.
package budget

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/birdtable/go-proc/load"
)

const (
	exitDone   = 0
	exitFailed = 1
	exitUsage  = 2
)

const usage = `usage: hatch budget --budgets <file> --row <regexp> --tier <name> --since <unix seconds> [--prefix <text>] [--window <minutes> [--limit <load> --loaded-when <which>] [--load-before <load>]] [--noun <words>] [--excused <words>] [--advice <text>]
       hatch budget --cpu --cpu-row <regexp> --budgets <file> --row <regexp> --tier <name> --since <unix seconds> [the flags above] -- <command> [args]
       hatch budget --load --window <minutes>

hatch holds no policy: the caller names each value.
  --budgets      the budgets file (required); the id of a row is --prefix then --tier
  --row          a regular expression for one row of the file, with the named groups id and limit,
                 the limit in seconds (required); each row is a match, a line of its own or several
  --tier         the budget to judge (required)
  --since        the second the tier started, in Unix seconds (required)
  --prefix       put before --tier to make the row id; unset, the id is --tier as given
  --window       the load average to read, 1, 5 or 15 minutes; unset, no load is read and an overrun
                 always fails
  --limit        the load above which an overrun is forgiven (needs --window and --loaded-when); unset,
                 the load excuses nothing
  --loaded-when  which sample must be above --limit (required with --limit):
                   before          the sample before the tier
                   end             the sample at its end
                   either          one of them: the end sample counts alone when none was taken before
                   before-and-end  both, so a tier with no sample before it is never excused
                 The end sample always holds the judged tier's own load. end and either therefore excuse a
                 tier by its own work, and are for a caller whose tier does not load the machine (it waits
                 on the network or on another host). To tell another job's load from the tier's own, pass
                 before or before-and-end with a sample from before the caller's run.
  --load-before  a load the caller sampled before its first unit of work on the machine (needs --window);
                 unset, none was sampled. A sample taken between two tiers of one run holds the earlier
                 tier's load and tells nothing about another job.
  --noun         what the load lines call the judged span (required with --limit); unset, the lines name none
  --excused      the words after "is over budget," on a forgiven overrun (required with --limit)
  --advice       the text that follows "is over its budget:" on a failure; unset, none is added
  --load         print the load average of --window and stop
  --cpu          judge the tier by the CPU time, user plus system, of the command after "--", which hatch
                 runs in a tree of its own, and not by wall time: CPU time is the same on a quiet machine
                 and a loaded one. The CPU row alone decides over or under: a tier over its wall limit with
                 its CPU under passes, and the output names the wall time and the load. The wall limit is
                 a hang guard only: a command still running at 10 times it is killed, and fails as hung. A tier with no CPU row is refused by name, and never
                 judged by wall time alone. A tree that cannot be read whole is a failure that says why
                 ("tree not read: ..."): a descendant that outlived the command on Unix, no job object on
                 Windows. It is never a pass.
  --cpu-row      a regular expression for one row of the CPU limits in the file, with the named groups
                 id and limit, the limit in seconds (required with --cpu); the id is --prefix then --tier
`

// Limit is the limit of the budget id in the file's text, in seconds, from the rows the
// expression row matches: each match names its id and its limit in the groups id and limit. An id
// with two rows has no limit: the first would win, and a second row added to
// raise a tier's ceiling would not be read. A file with CRLF line ends reads
// the same as one with LF.
func Limit(data []byte, row *regexp.Regexp, id string) (int, bool) {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	limit, found := 0, 0
	iid, ilim := row.SubexpIndex("id"), row.SubexpIndex("limit")
	for _, m := range row.FindAllSubmatch(data, -1) {
		if string(m[iid]) == id {
			n, err := strconv.Atoi(string(m[ilim]))
			if err != nil {
				return 0, false
			}
			limit, found = n, found+1
		}
	}
	return limit, found == 1
}

// LoadSource gives the load average of the machine now over window minutes (1, 5 or 15) and the
// number of CPUs it was read on, 0 when the source does not say.
type LoadSource func(ctx context.Context, window int) (load float64, cpus int, err error)

// Loaded says whether a tier counts as loaded, and by which sample. A sample
// that was not taken (before < 0) or could not be read (endErr) never counts:
// an unknown load excuses nothing. when is before, end, either or before-and-end:
// either counts the end sample alone when none was taken before, and
// before-and-end needs both samples above the limit.
func Loaded(before float64, end float64, endErr error, limit float64, when, noun string) (loaded bool, which string) {
	b := when != "end" && before >= 0 && before > limit
	e := when != "before" && endErr == nil && end > limit
	if when == "before-and-end" && !(b && e) {
		return false, ""
	}
	switch {
	case b && e:
		return true, fmt.Sprintf("load %.0f before %s and %.0f at its end, above the limit of %g", before, noun, end, limit)
	case b:
		return true, fmt.Sprintf("load %.0f before %s, above the limit of %g", before, noun, limit)
	case e:
		return true, fmt.Sprintf("load %.0f at the end of %s, above the limit of %g", end, noun, limit)
	}
	return false, ""
}

// Run is the budget command. It judges the wall time a tier took, from the
// second it started (--since, in Unix seconds) to now, against the limit of
// the row --prefix then --tier in the file --budgets names, read by the
// expression --row. It prints its lines on stdout, and fails over budget unless
// the tier was loaded: the load of --window minutes is above --limit at the
// sample --loaded-when names. With no --window or no --limit the load excuses
// nothing. --load alone prints the load average of --window, which a caller samples before its first unit
// of work on the machine and hands back as --load-before: a sample taken between two tiers of one run
// holds the earlier tier's load. The end sample always holds the judged tier's own load, so end and either
// excuse a tier by its own work; before and before-and-end, with a sample from before the run, tell another
// job's load from it.
func Run(ctx context.Context, args []string, now time.Time, load LoadSource, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("budget", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	budgets := fl.String("budgets", "", "the budgets file")
	rowExpr := fl.String("row", "", "one row of the file, with the groups id and limit")
	tier := fl.String("tier", "", "the budget to judge")
	prefix := fl.String("prefix", "", "put before --tier to make the row id")
	since := fl.Int64("since", 0, "the second the tier started, in Unix seconds")
	window := fl.Int("window", 0, "the load average to read, in minutes: 1, 5 or 15")
	limit := fl.Float64("limit", -1, "the load above which an overrun is forgiven")
	when := fl.String("loaded-when", "", "which sample must be above --limit: before, end, either or before-and-end")
	before := fl.Float64("load-before", -1, "the load sampled just before the tier started")
	noun := fl.String("noun", "", "what the load lines call the judged span")
	excused := fl.String("excused", "", "the words after \"is over budget,\" on a forgiven overrun")
	advice := fl.String("advice", "", "the text after \"is over its budget:\"")
	sample := fl.Bool("load", false, "print the load average and stop")
	cpu := fl.Bool("cpu", false, "judge the CPU time of the command after --")
	cpuRowExpr := fl.String("cpu-row", "", "one row of the CPU limits, with the groups id and limit")
	if err := fl.Parse(args); err != nil || (!*cpu && fl.NArg() != 0) {
		io.WriteString(stderr, usage)
		return exitUsage
	}
	set := map[string]bool{}
	fl.Visit(func(f *flag.Flag) { set[f.Name] = true })
	badWindow := set["window"] && *window != 1 && *window != 5 && *window != 15
	if *sample {
		if !set["window"] || badWindow {
			io.WriteString(stderr, usage)
			return exitUsage
		}
		l, _, err := load(ctx, *window)
		if err != nil {
			fmt.Fprintf(stderr, "hatch budget: %s\n", unreadable(err))
			return exitFailed
		}
		fmt.Fprintf(stdout, "%.2f\n", l)
		return exitDone
	}
	needsLoad := set["limit"] || set["load-before"] || set["loaded-when"]
	if *budgets == "" || *rowExpr == "" || *tier == "" || *since <= 0 || badWindow ||
		(needsLoad && !set["window"]) ||
		(set["limit"] && (!(*limit >= 0) || !(*when == "before" || *when == "end" || *when == "either" || *when == "before-and-end"))) ||
		(set["limit"] && (strings.TrimSpace(*noun) == "" || strings.TrimSpace(*excused) == "")) ||
		(set["loaded-when"] && !set["limit"]) ||
		(*cpu && (*cpuRowExpr == "" || fl.NArg() == 0)) || (!*cpu && set["cpu-row"]) {
		io.WriteString(stderr, usage)
		return exitUsage
	}
	row, err := regexp.Compile("(?m)" + *rowExpr)
	if err != nil || row.SubexpIndex("id") < 0 || row.SubexpIndex("limit") < 0 {
		fmt.Fprintf(stderr, "hatch budget: --row must be an expression with the named groups id and limit: %v\n", err)
		io.WriteString(stderr, usage)
		return exitUsage
	}
	data, err := os.ReadFile(*budgets)
	if err != nil {
		fmt.Fprintf(stderr, "hatch budget: %v\n", err)
		return exitFailed
	}
	id := *prefix + *tier
	limitSecs, ok := Limit(data, row, id)
	if !ok {
		fmt.Fprintf(stderr, "hatch budget: %s has no single limit for %s\n", *budgets, id)
		return exitFailed
	}
	var cpuLimit int
	if *cpu {
		crow, err := regexp.Compile("(?m)" + *cpuRowExpr)
		if err != nil || crow.SubexpIndex("id") < 0 || crow.SubexpIndex("limit") < 0 {
			fmt.Fprintf(stderr, "hatch budget: --cpu-row must be an expression with the named groups id and limit: %v\n", err)
			io.WriteString(stderr, usage)
			return exitUsage
		}
		if cpuLimit, ok = Limit(data, crow, id); !ok {
			fmt.Fprintf(stderr, "hatch budget: --cpu: %s has no single CPU limit for %s, and a tier is never judged by wall time alone under --cpu\n", *budgets, id)
			return exitFailed
		}
	}
	cpuFailed := false
	if *cpu {
		began := time.Now()
		code, used, err := measure(ctx, fl.Args(), hangAfter(limitSecs), stdout, stderr)
		now = now.Add(time.Since(began))
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "hatch budget: %s: %v\n", id, err)
			return exitFailed
		case code != 0:
			fmt.Fprintf(stderr, "hatch budget: %s: the command exited %d\n", id, code)
			return exitFailed
		}
		fmt.Fprintf(stdout, "budget: %s used %.1f s of CPU of %d s\n", id, used.Seconds(), cpuLimit)
		if used.Seconds() > float64(cpuLimit) {
			fmt.Fprintf(stderr, "hatch budget: %s is over its CPU budget\n", id)
			cpuFailed = true
		}
	}
	took := int(now.Unix() - *since)
	if took < 0 {
		fmt.Fprintf(stderr, "hatch budget: %s: --since %d is %d s after now, so took is negative: the clock went backwards, and a negative took is never a pass\n", id, *since, -took)
		return exitFailed
	}
	fmt.Fprintf(stdout, "budget: %s took %d s of %d s\n", id, took, limitSecs)
	span := ""
	if *noun != "" {
		span = " " + *noun
	}
	var end float64
	var cpus int
	var endErr error
	if set["window"] {
		end, cpus, endErr = load(ctx, *window)
		switch {
		case endErr != nil:
			fmt.Fprintf(stdout, "budget: %s\n", unreadable(endErr))
		case *before >= 0:
			fmt.Fprintf(stdout, "budget: load %.1f before%s, %.1f at its end%s\n", *before, span, end, onCPUs(cpus))
		default:
			fmt.Fprintf(stdout, "budget: load %.1f at its end%s; none was sampled before%s\n", end, onCPUs(cpus), span)
		}
	}
	if *cpu {
		if took > limitSecs {
			fmt.Fprintf(stdout, "budget: %s is over its wall limit of %d s, and passes or fails on its CPU time alone: --cpu judges CPU time, and wall time only kills a hung command\n", id, limitSecs)
		}
		return verdict(cpuFailed)
	}
	if took <= limitSecs {
		return exitDone
	}
	if set["limit"] {
		if loaded, which := Loaded(*before, end, endErr, *limit, *when, *noun); loaded {
			fmt.Fprintf(stdout, "budget: %s is over budget, %s: %s\n", id, *excused, which)
			return exitDone
		}
	}
	msg := fmt.Sprintf("hatch budget: %s is over its budget", id)
	if *advice != "" {
		msg += ": " + *advice
	}
	fmt.Fprintln(stderr, msg)
	if errors.Is(endErr, ErrNoLoadAverage) {
		// No load average here, so the overrun is not known to be load-free either.
		fmt.Fprintf(stderr, "hatch budget: %s\n", unreadable(endErr))
	}
	return exitFailed
}

// hangFactor is how many times its wall limit a command under --cpu may run before it is killed as hung:
// a loaded machine slows a tier without hanging it, so the wall limit itself is no deadline.
const hangFactor = 10

// hangAfter is when a command whose tier has the wall limit given is killed as hung; a row replaces it.
var hangAfter = func(limitSecs int) time.Duration { return time.Duration(limitSecs) * hangFactor * time.Second }

// verdict is the exit code of a tier whose wall time is judged: it fails when its CPU time was over.
func verdict(cpuFailed bool) int {
	if cpuFailed {
		return exitFailed
	}
	return exitDone
}

// onCPUs is the CPU count as a fact at the end of a load line, or nothing when the source gave none.
func onCPUs(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return ", on 1 CPU"
	}
	return fmt.Sprintf(", on %d CPUs", n)
}

// ErrNoLoadAverage is a system that has no load average (Windows).
var ErrNoLoadAverage = errors.New("no load average")

// unreadable says why the load is unknown. For a system with no load average the reason is go-proc's
// own refusal, which HostLoad wraps after ErrNoLoadAverage; the message names no system.
func unreadable(err error) string {
	if errors.Is(err, ErrNoLoadAverage) {
		if err != ErrNoLoadAverage {
			return "load unknown: " + strings.TrimPrefix(err.Error(), ErrNoLoadAverage.Error()+": ")
		}
		return "load unknown"
	}
	return "the load cannot be read: " + err.Error()
}

// HostLoad reads the load average over window minutes (1, 5 or 15) through go-proc/load, with
// the CPU count it gives (Reading.CPUs). A
// system that has none, or one that cannot be read, gives an error that matches
// ErrNoLoadAverage (Windows), with go-proc's own words after it, so the load is unknown there
// and excuses nothing.
func HostLoad(ctx context.Context, window int) (float64, int, error) {
	r, err := load.Read()
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %w", ErrNoLoadAverage, err)
	}
	switch window {
	case 5:
		return r.Five, r.CPUs, nil
	case 15:
		return r.Fifteen, r.CPUs, nil
	}
	return r.One, r.CPUs, nil
}
