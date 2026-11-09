package beads

import (
	"fmt"
	"github.com/example-user/commander/internal/ownercompat"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A "waiting on" record says what an owner's turn ended on, so cmdrd can wake
// it. It is a bead label (br labels allow only letters, digits, - _ :):
//
//	waiting:<unix since>:ci:4101               CI of PR #4101 in the project repo
//	waiting:<unix since>:ci:owner:repo:4101    CI of a PR in another repo
//	waiting:<unix since>:main-ci:<sha>         CI on main for a commit (project repo)
//	waiting:<unix since>:main-ci:owner:repo:<sha>
//	waiting:<unix since>:prod-tag:<sha>        a prod tag on a commit containing <sha>
//	waiting:<unix since>:prod-tag:owner:repo:<sha>
//	waiting:<unix since>:review:<login>        a requested reviewer
//	waiting:<unix since>:until:<unix target>:<label>   a time; <label> is EscapeLabel'd (may be empty)
//	waiting:<unix since>:user
//	waiting:<unix since>:operator:<role>
//
// WaitCheckedPrefix starts the label cmdrd stamps each poll (wait-checked:<unix>):
// when it last looked at the bead's waits. It never wakes anyone. The old
// wait-nudged label is only cleared where found.
const (
	WaitLabelPrefix   = "waiting:"
	WaitCheckedPrefix = "wait-checked:"
	WaitNudgedLabel   = "wait-nudged"

	WaitCI       = "ci"
	WaitMainCI   = "main-ci"
	WaitProdTag  = "prod-tag"
	WaitReview   = "review"
	WaitUser     = "user"
	WaitOperator = "operator"
	WaitUntil    = "until"
)

// ManagerCloseWait labels the time wait cmdrd puts on a step whose Operator
// finished: the Manager judges the report and closes or reopens the step by then.
const ManagerCloseWait = "Manager to close"

// ManagerCloseWithin is how long a finished step waits on its Manager. Past it
// cmdrd mails the Manager again and the board flags the step. The step is never
// closed for the Manager: it judges the report.
const ManagerCloseWithin = time.Hour

// ManagerDispatchWait retains custody between an approval and real dispatch.
const ManagerDispatchWait = "Manager dispatch"

// ManagerStartWait retains custody of ready work a close or create left with
// nothing holding it, until the Manager starts it (spawn, wait, card).
const ManagerStartWait = "Manager to start"

// IsManagerHandoff identifies receipts that survive their reminder deadline.
func IsManagerHandoff(w Wait) bool {
	return w.Kind == WaitUntil && (w.Ref == ManagerCloseWait || w.Ref == ManagerDispatchWait || w.Ref == ManagerStartWait)
}

// IsManagerDispatch identifies the receipts a real successor (a spawn under
// the step, a wait, a card, an Operator owner) accepts; Manager-only edits keep them.
func IsManagerDispatch(w Wait) bool {
	return w.Kind == WaitUntil && (w.Ref == ManagerDispatchWait || w.Ref == ManagerStartWait)
}

// IsSettleMarker reports whether label is one cmdrd wrote when a step's
// Operator ended (the died marker, the wait on the Manager). Claiming or
// re-planning the step clears them.
func IsSettleMarker(label string) bool {
	if label == OperatorDiedLabel || strings.HasPrefix(label, "handoff-notified:") {
		return true
	}
	w, ok := ParseWaitLabel(label)
	return ok && IsManagerHandoff(w)
}

// Wait is one thing a bead's owner is waiting on.
type Wait struct {
	Kind string
	Repo string // ci, main-ci, prod-tag: owner/name, "" = the project's repo
	PR   int    // ci only
	Sha  string // main-ci, prod-tag: the commit (hex, 7-40 chars)
	Ref  string // review: login; operator: role; until: the label (may be empty)
	// Until is the target time of an until wait.
	Until time.Time
	Since time.Time
	// Checked is when cmdrd last looked (the bead's wait-checked label); zero = never.
	Checked time.Time
	// Cut: an until wait's label was cut to fit MaxLabelLen; Text marks it.
	Cut bool
}

// CheckedLabel is the label recording a poll at t.
func CheckedLabel(t time.Time) string {
	return WaitCheckedPrefix + strconv.FormatInt(t.Unix(), 10)
}

// ParseCheckedLabel reads a wait-checked label.
func ParseCheckedLabel(label string) (time.Time, bool) {
	rest, ok := strings.CutPrefix(label, WaitCheckedPrefix)
	secs, err := strconv.ParseInt(rest, 10, 64)
	if !ok || err != nil {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

var (
	waitRefUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]`)
	waitShaRe     = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// EscapeLabel makes a free-text value safe in a br label (alphanumeric, - and :
// only): any other byte, "_" included, becomes "_" + two hex digits.
func EscapeLabel(v string) string {
	var sb strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == ':' {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, "_%02x", c)
		}
	}
	return sb.String()
}

// UnescapeLabel is the inverse of EscapeLabel.
func UnescapeLabel(v string) string {
	var sb strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '_' && i+2 < len(v) {
			if b, err := strconv.ParseUint(v[i+1:i+3], 16, 8); err == nil {
				sb.WriteByte(byte(b))
				i += 2
				continue
			}
		}
		sb.WriteByte(v[i])
	}
	return sb.String()
}

// parseRepoSha reads "<sha>" or "<owner/repo>@<sha>".
func parseRepoSha(arg, ref string) (repo, sha string, err error) {
	repo, sha, hasRepo := strings.Cut(ref, "@")
	if !hasRepo {
		repo, sha = "", ref
	}
	if !waitShaRe.MatchString(sha) {
		return "", "", fmt.Errorf("--waiting %q: want a hex commit sha (7-40 chars)", arg)
	}
	if repo != "" && (strings.Count(repo, "/") != 1 || strings.ContainsAny(repo, ". :@")) {
		return "", "", fmt.Errorf("--waiting %q: repo must be owner/name without dots", arg)
	}
	return repo, sha, nil
}

// ParseWait reads a --waiting argument: ci:#N, ci:<owner/repo>#N, main-ci:<sha>,
// main-ci:<owner/repo>@<sha>, prod-tag:<sha> (same forms), review:<login>,
// user, operator:<role>, until:<RFC3339>[:label]. Since is left zero.
func ParseWait(arg string) (Wait, error) {
	kind, ref, _ := strings.Cut(strings.TrimSpace(arg), ":")
	kind = ownercompat.Normalize(kind)
	w := Wait{Kind: kind}
	switch kind {
	case WaitCI:
		repo, num, ok := strings.Cut(ref, "#")
		n, err := strconv.Atoi(num)
		if !ok || err != nil || n <= 0 {
			return w, fmt.Errorf("--waiting %q: want ci:#N or ci:<owner/repo>#N", arg)
		}
		if repo != "" && (strings.Count(repo, "/") != 1 || strings.ContainsAny(repo, ". :")) {
			return w, fmt.Errorf("--waiting %q: repo must be owner/name without dots", arg)
		}
		w.Repo, w.PR = repo, n
	case WaitMainCI, WaitProdTag:
		repo, sha, err := parseRepoSha(arg, ref)
		if err != nil {
			return w, err
		}
		w.Repo, w.Sha = repo, sha
	case WaitReview, WaitOperator:
		if ref == "" || waitRefUnsafe.MatchString(ref) {
			return w, fmt.Errorf("--waiting %q: want %s:<name> (letters, digits, - and _)", arg, kind)
		}
		w.Ref = ref
	case WaitUser:
		if ref != "" {
			return w, fmt.Errorf("--waiting %q: want just user", arg)
		}
	case WaitUntil:
		// An RFC3339 time holds colons itself, so the label starts after the
		// time: try each colon from the right until the head parses.
		stamp, label := ref, ""
		var t time.Time
		var err error
		for {
			if t, err = time.Parse(time.RFC3339, stamp); err == nil {
				break
			}
			i := strings.LastIndex(stamp, ":")
			if i < 0 {
				return w, fmt.Errorf("--waiting %q: want until:<RFC3339 time>[:label]", arg)
			}
			stamp, label = stamp[:i], ref[i+1:]
		}
		w.Until, w.Ref = t, strings.TrimSpace(label)
	default:
		return w, fmt.Errorf("--waiting %q: kind must be ci, main-ci, prod-tag, review, user, operator or until", arg)
	}
	return w, nil
}

// ReadableUntilLabel reports whether a time wait's label reads as plain words
// (the user sees "Waiting for <label>"), not a slug.
func ReadableUntilLabel(label string) bool {
	label = strings.TrimSpace(label)
	return label != "" && strings.Contains(label, " ") && len(label) <= 80
}

// Untyped reports a time wait whose label is a slug: it stands in for
// something cmdrd cannot watch (a person, a release), so nothing clears it
// but the clock. An unlabelled time wait is just a time.
func (w Wait) Untyped() bool {
	return w.Kind == WaitUntil && strings.TrimSpace(w.Ref) != "" && !strings.Contains(w.Ref, " ")
}

// Label is the bead label that records w.
func (w Wait) Label() string {
	if l := w.longLabel(); len(l) <= MaxLabelLen && !w.Cut {
		return l
	}
	return w.compactLabel()
}

// MaxLabelLen is br's limit on a label; a longer one is refused on write.
const MaxLabelLen = 50

// longBody is the part of the long form after waiting:<since>:.
func (w Wait) longBody() string {
	body := w.Kind
	switch w.Kind {
	case WaitCI:
		if w.Repo != "" {
			body += ":" + strings.Replace(w.Repo, "/", ":", 1)
		}
		body += ":" + strconv.Itoa(w.PR)
	case WaitMainCI, WaitProdTag:
		if w.Repo != "" {
			body += ":" + strings.Replace(w.Repo, "/", ":", 1)
		}
		body += ":" + w.Sha
	case WaitReview, WaitOperator:
		body += ":" + w.Ref
	case WaitUntil:
		body += ":" + strconv.FormatInt(w.Until.Unix(), 10)
		if w.Ref != "" {
			body += ":" + EscapeLabel(w.Ref)
		}
	}
	return body
}

func (w Wait) longLabel() string {
	return fmt.Sprintf("%s%d:%s", WaitLabelPrefix, w.Since.Unix(), w.longBody())
}

// compactWaitPrefix starts the short form of a wait label, written only when
// the long form would not fit MaxLabelLen (long Operator roles, labelled time
// waits): base-36 times, a two-letter kind, an Operator role without
// "operator-", a time wait's label cut to fit.
const compactWaitPrefix = "w:"

var waitKindCode = map[string]string{WaitCI: "ci", WaitMainCI: "mc", WaitProdTag: "pt", WaitReview: "rv", WaitUser: "us", WaitOperator: "op", WaitUntil: "un"}

func (w Wait) compactLabel() string {
	head := compactWaitPrefix + strconv.FormatInt(w.Since.Unix(), 36) + ":" + waitKindCode[w.Kind]
	switch w.Kind {
	case WaitOperator:
		return head + ":" + strings.TrimPrefix(w.Ref, "operator-")
	case WaitUntil:
		head += ":" + strconv.FormatInt(w.Until.Unix(), 36)
		ref := []rune(w.Ref)
		cut := false
		for {
			l := head
			if len(ref) > 0 {
				l += ":" + EscapeLabel(string(ref))
			}
			if len(l) <= MaxLabelLen || len(ref) == 0 {
				if s := string(ref); cut && strings.Contains(strings.TrimRight(s, " "), " ") {
					// Cut at a word boundary, keeping the space: a trailing space marks the cut.
					return head + ":" + EscapeLabel(s[:strings.LastIndex(strings.TrimRight(s, " "), " ")+1])
				}
				return l
			}
			ref, cut = ref[:len(ref)-1], true
		}
	}
	// Other kinds keep their long body behind the short head.
	_, body, _ := strings.Cut(w.longBody(), ":")
	if body == "" {
		return head
	}
	return head + ":" + body
}

// ParseWaitLabel is the inverse of Label; ok is false for any other label.
func ParseWaitLabel(label string) (Wait, bool) {
	if c, ok := strings.CutPrefix(label, compactWaitPrefix); ok {
		w, ok := parseCompactWait(c)
		// A trailing space marks a cut label; older cuts filled the label to its limit.
		if ok && w.Kind == WaitUntil && w.Ref != "" && (strings.HasSuffix(w.Ref, " ") || len(label) >= MaxLabelLen-2) {
			w.Cut = true
		}
		return w, ok
	}
	rest, ok := strings.CutPrefix(label, WaitLabelPrefix)
	if !ok {
		return Wait{}, false
	}
	unix, body, ok := strings.Cut(rest, ":")
	secs, err := strconv.ParseInt(unix, 10, 64)
	if !ok || err != nil {
		return Wait{}, false
	}
	parts := strings.Split(body, ":")
	w := Wait{Kind: ownercompat.Normalize(parts[0]), Since: time.Unix(secs, 0)}
	switch w.Kind {
	case WaitUntil:
		if len(parts) < 2 {
			return Wait{}, false
		}
		target, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return Wait{}, false
		}
		w.Until = time.Unix(target, 0)
		w.Ref = UnescapeLabel(strings.Join(parts[2:], ":"))
	case WaitCI:
		n, err := strconv.Atoi(parts[len(parts)-1])
		if err != nil || (len(parts) != 2 && len(parts) != 4) {
			return Wait{}, false
		}
		w.PR = n
		if len(parts) == 4 {
			w.Repo = parts[1] + "/" + parts[2]
		}
	case WaitMainCI, WaitProdTag:
		if (len(parts) != 2 && len(parts) != 4) || !waitShaRe.MatchString(parts[len(parts)-1]) {
			return Wait{}, false
		}
		w.Sha = parts[len(parts)-1]
		if len(parts) == 4 {
			w.Repo = parts[1] + "/" + parts[2]
		}
	case WaitReview, WaitOperator:
		if len(parts) != 2 {
			return Wait{}, false
		}
		w.Ref = parts[1]
	case WaitUser:
		if len(parts) != 1 {
			return Wait{}, false
		}
	default:
		return Wait{}, false
	}
	return w, true
}

// WaitsOf lists the waits recorded on a bead's labels, oldest first.
func WaitsOf(labels []string) []Wait {
	var out []Wait
	var checked time.Time
	for _, l := range labels {
		if w, ok := ParseWaitLabel(l); ok {
			out = append(out, w)
		} else if t, ok := ParseCheckedLabel(l); ok {
			checked = t
		}
	}
	for i := range out {
		out[i].Checked = checked
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Since.Before(out[j-1].Since); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Text is the short form shown in the sidebar: "CI #4101", "review (reviewer-a)", "you".
func (w Wait) Text() string {
	switch w.Kind {
	case WaitCI:
		if w.Repo != "" {
			return fmt.Sprintf("CI %s#%d", w.Repo, w.PR)
		}
		return fmt.Sprintf("CI #%d", w.PR)
	case WaitMainCI:
		return "main CI " + shortSha(w.Sha)
	case WaitProdTag:
		return "prod tag " + shortSha(w.Sha)
	case WaitReview:
		return "review (" + w.Ref + ")"
	case WaitUser:
		return "you"
	case WaitUntil:
		if w.Ref == "" {
			return "time"
		}
		if w.Cut {
			ref := w.Ref
			// Labels written before cuts kept to word boundaries end mid-word: drop the stub.
			if i := strings.LastIndex(ref, " "); i > 0 && !strings.HasSuffix(ref, " ") {
				ref = ref[:i]
			}
			return strings.TrimSpace(ref) + "…"
		}
	}
	return w.Ref
}

// WaitsText joins waits for one row: reviewers merge into "review (reviewer-a, reviewer-b)".
func WaitsText(ws []Wait) string {
	var parts, reviewers []string
	reviewAt := -1
	for _, w := range ws {
		if w.Kind == WaitReview {
			if reviewAt < 0 {
				reviewAt = len(parts)
				parts = append(parts, "")
			}
			reviewers = append(reviewers, w.Ref)
			continue
		}
		parts = append(parts, w.Text())
	}
	if reviewAt >= 0 {
		parts[reviewAt] = "review (" + strings.Join(reviewers, ", ") + ")"
	}
	return strings.Join(parts, " · ")
}

func shortSha(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// parseCompactWait reads the short form back into the long one and parses that.
func parseCompactWait(c string) (Wait, bool) {
	parts := strings.SplitN(c, ":", 3)
	if len(parts) < 2 {
		return Wait{}, false
	}
	since, err := strconv.ParseInt(parts[0], 36, 64)
	if err != nil {
		return Wait{}, false
	}
	kind := ""
	if parts[1] == "er" {
		kind = WaitUser
	}
	for k, code := range waitKindCode {
		if code == parts[1] {
			kind = k
		}
	}
	if kind == "" {
		return Wait{}, false
	}
	rest := ""
	if len(parts) == 3 {
		rest = parts[2]
	}
	switch kind {
	case WaitOperator:
		rest = "operator-" + rest
	case WaitUntil:
		target, label, _ := strings.Cut(rest, ":")
		t, err := strconv.ParseInt(target, 36, 64)
		if err != nil {
			return Wait{}, false
		}
		rest = strconv.FormatInt(t, 10)
		if label != "" {
			rest += ":" + label
		}
	}
	long := fmt.Sprintf("%s%d:%s", WaitLabelPrefix, since, kind)
	if rest != "" {
		long += ":" + rest
	}
	return ParseWaitLabel(long)
}

var reviewTimer = regexp.MustCompile(`(?i)\b(review|reviews|reviewed|reviewer|reviewers|approval|approve|approved)\b.*\b(pr|pull request)\b|\b(pr|pull request)\b.*\b(review|reviews|reviewed|reviewer|reviewers|approval|approve|approved)\b`)

// ReviewTimerLabel reports a time wait's label that is about a PR's review or
// approval: cmdrd watches reviews itself, so a timer standing in for one is wrong.
func ReviewTimerLabel(label string) bool { return reviewTimer.MatchString(label) }
