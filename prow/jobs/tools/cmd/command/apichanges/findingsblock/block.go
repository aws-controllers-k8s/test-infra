// Package findingsblock is the machine-readable list of findings that
// detect-api-changes embeds, as an HTML comment, in each issue it files. The
// ACK Autopilot issue finder parses it. Prose in the issue is for people and may
// change freely; this block is the only part a program may read.
//
// Changing the format means bumping Version: Parse rejects any other version, so
// an old reader skips a new block instead of misreading it.
package findingsblock

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	Begin   = "<!-- ack-api-change-findings "
	End     = "-->"
	Version = "v1"
)

// Buckets name the work a finding needs. They match the issue's section headers.
const (
	BucketNewResource  = "new-resource"
	BucketManualReview = "manual-review"
	BucketNeedsConfig  = "needs-config"
	BucketRegen        = "regen"
)

// bucketOrder puts the work a person must do first, so truncation drops
// "regen" lines (which one SDK bump covers anyway) before anything else.
var bucketOrder = map[string]int{
	BucketNewResource:  0,
	BucketManualReview: 1,
	BucketNeedsConfig:  2,
	BucketRegen:        3,
}

// Line is one finding. Class, Kind and Subject are the identity the issue
// fingerprint hashes; Bucket and Work describe the work.
type Line struct {
	Class, Kind, Subject, Bucket, Work string
}

// Header names the service and the two SDK versions the detector compared.
type Header struct {
	Service, From, To string
}

// Block is a parsed findings block. Omitted counts lines dropped to fit the
// issue, so a reader must not treat a missing line as a finding that is gone.
type Block struct {
	Header
	Count   int
	Omitted int
	Lines   []Line
}

// quote is strconv.Quote with "--" escaped, so a value can never contain the
// "-->" that would end the HTML comment. strconv.Unquote reverses it.
func quote(s string) string {
	return strings.ReplaceAll(strconv.Quote(s), "--", `-\x2d`)
}

func headerLine(h Header, count, omitted int) string {
	return fmt.Sprintf("%s%s service=%s from=%s to=%s count=%d omitted=%d\n",
		Begin, Version, quote(h.Service), quote(h.From), quote(h.To), count, omitted)
}

func (l Line) render() string {
	return strings.Join([]string{
		quote(l.Class), quote(l.Kind), quote(l.Subject), quote(l.Bucket), quote(l.Work),
	}, "|") + "\n"
}

func less(a, b Line) bool {
	if bucketOrder[a.Bucket] != bucketOrder[b.Bucket] {
		return bucketOrder[a.Bucket] < bucketOrder[b.Bucket]
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Subject != b.Subject {
		return a.Subject < b.Subject
	}
	return a.Class < b.Class
}

// Format renders lines as a block of at most limit bytes and returns it with
// the number of lines left out. It returns "" when not even the header fits.
func Format(h Header, lines []Line, limit int) (string, int) {
	sorted := append([]Line(nil), lines...)
	sort.SliceStable(sorted, func(i, j int) bool { return less(sorted[i], sorted[j]) })

	// The final header is never longer than this one: its counts are at most
	// len(sorted).
	reserve := len(headerLine(h, len(sorted), len(sorted))) + len(End) + len("\n")
	if reserve > limit {
		return "", len(sorted)
	}
	var body strings.Builder
	kept := 0
	for _, l := range sorted {
		r := l.render()
		if reserve+body.Len()+len(r) > limit {
			break
		}
		body.WriteString(r)
		kept++
	}
	omitted := len(sorted) - kept
	return headerLine(h, kept, omitted) + body.String() + End + "\n", omitted
}

const quoted = `("(?:[^"\\]|\\.)*")`

var (
	headerRE = regexp.MustCompile(`^` + regexp.QuoteMeta(Begin) + Version +
		` service=` + quoted + ` from=` + quoted + ` to=` + quoted +
		` count=(\d+) omitted=(\d+)$`)
	lineRE = regexp.MustCompile(`^` + quoted + `\|` + quoted + `\|` + quoted + `\|` + quoted + `\|` + quoted + `$`)
)

func unquoteAll(in []string) ([]string, error) {
	out := make([]string, len(in))
	for i, s := range in {
		v, err := strconv.Unquote(s)
		if err != nil {
			return nil, fmt.Errorf("bad quoted value %s: %w", s, err)
		}
		out[i] = v
	}
	return out, nil
}

// Parse reads the block from an issue body. found is false when the body has no
// block. More than one block, an unknown version, or any malformed line is an
// error: the caller must skip the issue rather than guess.
func Parse(body string) (b Block, found bool, err error) {
	switch n := strings.Count(body, Begin); {
	case n == 0:
		return Block{}, false, nil
	case n > 1:
		return Block{}, true, fmt.Errorf("%d findings blocks, want 1", n)
	}
	rest := strings.ReplaceAll(body[strings.Index(body, Begin):], "\r\n", "\n")
	lines := strings.Split(rest, "\n")

	m := headerRE.FindStringSubmatch(strings.TrimRight(lines[0], " \t"))
	if m == nil {
		return Block{}, true, fmt.Errorf("malformed findings header %q", lines[0])
	}
	h, err := unquoteAll(m[1:4])
	if err != nil {
		return Block{}, true, err
	}
	b.Header = Header{Service: h[0], From: h[1], To: h[2]}
	b.Count, _ = strconv.Atoi(m[4])
	b.Omitted, _ = strconv.Atoi(m[5])

	closed := false
	for _, raw := range lines[1:] {
		raw = strings.TrimRight(raw, " \t")
		if raw == End {
			closed = true
			break
		}
		lm := lineRE.FindStringSubmatch(raw)
		if lm == nil {
			return Block{}, true, fmt.Errorf("malformed findings line %q", raw)
		}
		f, err := unquoteAll(lm[1:])
		if err != nil {
			return Block{}, true, err
		}
		b.Lines = append(b.Lines, Line{Class: f[0], Kind: f[1], Subject: f[2], Bucket: f[3], Work: f[4]})
	}
	if !closed {
		return Block{}, true, fmt.Errorf("findings block is not terminated by %q", End)
	}
	if len(b.Lines) != b.Count {
		return Block{}, true, fmt.Errorf("findings block says count=%d but has %d lines", b.Count, len(b.Lines))
	}
	return b, true, nil
}
