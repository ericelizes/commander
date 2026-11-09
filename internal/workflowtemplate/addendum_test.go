package workflowtemplate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAddendumReferenceIntegrity(t *testing.T) {
	raw := `{"id":"record-1"}`
	ref := AddendumRef{ID: "record-1", SHA256: TextDigest(raw)}
	var got map[string]string
	if err := DecodeAddendumPayload(raw, ref, &got); err != nil || got["id"] != ref.ID {
		t.Fatalf("decode: %v, %v", got, err)
	}
	for _, altered := range []string{raw + " ", `{"id":"record-2"}`} {
		if err := DecodeAddendumPayload(altered, ref, &got); err == nil {
			t.Fatal("accepted altered exact bytes")
		}
	}
	for _, bad := range []AddendumRef{{}, {ID: "record-1", SHA256: "aa"}, {ID: "record-1", SHA256: strings.ToUpper(ref.SHA256)}} {
		if bad.Validate() == nil {
			t.Fatal("accepted malformed reference")
		}
	}
}

func TestUnresolvedAddendumCannotOverrideVerdict(t *testing.T) {
	var p Proof
	if err := json.Unmarshal([]byte(`{"review_bead":"review-task","addendum":{"id":"record-1","sha256":"`+TextDigest("record")+`"}}`), &p); err != nil {
		t.Fatal(err)
	}
	p.Head = "head"
	p.NoFindings = true
	reader := func(context.Context, string) (Review, error) {
		return Review{ID: p.ReviewBead, Status: "closed", Owner: "operator-review"}, nil
	}
	if _, err := reviewProof(context.Background(), nil, reader, "", Head{SHA: "head"}, &p); err == nil || !strings.Contains(err.Error(), "resolver unavailable") {
		t.Fatalf("unresolved addendum: %v", err)
	}
}

func TestAddendumVerdictAccountsForEveryReport(t *testing.T) {
	head := strings.Repeat("a", 40)
	original := "Head: " + head + "\nExtra failed-peer seal only\nNOT CLEAN"
	for _, tc := range []struct {
		name     string
		comments []string
		ok       bool
	}{
		{"sole withdrawn objection", []string{original}, true},
		{"later real finding", []string{original, "Head: " + head + "\nReal finding\nNOT CLEAN"}, false},
		{"earlier real finding", []string{"Head: " + head + "\nEarlier finding\nNOT CLEAN", original}, false},
		{"missing original", nil, false},
		{"duplicate original", []string{original, original}, false},
		{"stale other report", []string{original, "Head: stale\nCLEAN"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := AddendumVerdict(Review{Comments: tc.comments}, head, original); (err == nil) != tc.ok {
				t.Fatalf("%v", err)
			}
		})
	}
}

// The retained report is excused only as a NOT CLEAN: one that opens as a typed
// result is still read as one, so it cannot carry an unparsed finding past the door.
func TestAddendumVerdictReadsARetainedReportAsTyped(t *testing.T) {
	head := strings.Repeat("a", 40)
	original := ResultPrefix + `{"head":"` + head + `"}` + "\nHead: " + head + "\nNOT CLEAN"
	if err := AddendumVerdict(Review{Comments: []string{original}}, head, original); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("typed-looking retained report: %v", err)
	}
}

func TestAddendumAssessmentRejectsIncompleteClaims(t *testing.T) {
	d := TextDigest("synthetic")
	a := AddendumAssessment{Original: CommentRef{Bead: "review-task", ID: "comment-1", SHA256: d}, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), RunID: "synthetic-run", ManifestSHA256: d, TranscriptSHA256: d, SkillSHA256: d, PeerRuleSHA256: d, RecoveryRuleSHA256: d, OriginalBindingSHA256: d, PeerJobID: "synthetic-job", PeerStartEpoch: 100, Rule: WithdrawnPeerSeal, SoleObjection: true, NoFindings: true, MandatoryCoverageComplete: true, ValidatorsComplete: true, PeerProducedNoReview: true, QuotaFallbackAuthorized: true, StartPersistedBeforeLocal: true}
	test := func(a AddendumAssessment) error {
		b, _ := json.Marshal(a)
		_, err := ParseAssessment(AssessmentPrefix + string(b))
		return err
	}
	if err := test(a); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*AddendumAssessment){
		func(a *AddendumAssessment) { a.Rule = "arbitrary-override" }, func(a *AddendumAssessment) { a.SoleObjection = false }, func(a *AddendumAssessment) { a.NoFindings = false }, func(a *AddendumAssessment) { a.MandatoryCoverageComplete = false }, func(a *AddendumAssessment) { a.ValidatorsComplete = false }, func(a *AddendumAssessment) { a.PeerProducedNoReview = false }, func(a *AddendumAssessment) { a.QuotaFallbackAuthorized = false }, func(a *AddendumAssessment) { a.StartPersistedBeforeLocal = false }, func(a *AddendumAssessment) { a.ManifestSHA256 = "changed" }, func(a *AddendumAssessment) { a.Head = "stale" }, func(a *AddendumAssessment) { a.PeerStartEpoch = 0 },
	} {
		copy := a
		mutate(&copy)
		if test(copy) == nil {
			t.Fatal("incomplete assessment accepted")
		}
	}
}
