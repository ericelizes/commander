package managerguard

// Segment is one simple command of a shell line as the guards parse it: its
// words, its redirect targets and the bodies of its $(...) substitutions.
type Segment struct {
	Words     []string
	Redirects []string
	Subs      []string
}

// Segments splits line like the Manager guard does, for other guards that
// judge each command with its redirects (the Mac's read-only place guard).
func Segments(line string) []Segment {
	var out []Segment
	for _, seg := range parse(line) {
		out = append(out, Segment{Words: seg.words, Redirects: seg.redirects, Subs: seg.subs})
	}
	return out
}

// PureSegment reports whether one command only reads, by the Manager guard's
// read lists (git read subcommands, sed without w/e, no interpreters).
func PureSegment(words []string, hasSubs bool) bool { return pureSegment(words, hasSubs) }

// Unwrap drops leading assignments and wrappers (env, timeout, nice ...) and
// returns the command words.
func Unwrap(words []string) []string { return unwrap(words, true) }

// BaseName is a command word without its directory.
func BaseName(w string) string { return baseName(w) }

// UnwrapChecking checks each command and wrapper before processing its options.
// A refused wrapper is never expanded; traversal stops immediately.
func UnwrapChecking(words []string, refuse func(string) bool) []string {
	return unwrapChecking(words, true, refuse)
}
