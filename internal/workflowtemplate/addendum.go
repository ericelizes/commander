package workflowtemplate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// AddendumRef names an append-only supported-door record. It never replaces
// ReviewBead or changes the original report's verdict.
type AddendumRef struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

const AddendumPrefix = "workflow-review-addendum@v1 "

// TextDigest hashes exact stored bytes, without whitespace normalization.
func TextDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func (r AddendumRef) Validate() error {
	b, err := hex.DecodeString(r.SHA256)
	if r.ID == "" || err != nil || len(b) != sha256.Size || hex.EncodeToString(b) != r.SHA256 {
		return fmt.Errorf("addendum requires a supported record id and lowercase SHA256")
	}
	return nil
}

// DecodeAddendumPayload checks the digest before decoding the canonical record.
// A caller's supplied digest is an integrity anchor, not principal authentication.
func DecodeAddendumPayload(raw string, ref AddendumRef, dst any) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if TextDigest(raw) != ref.SHA256 {
		return fmt.Errorf("addendum record digest differs")
	}
	return json.Unmarshal([]byte(raw), dst)
}

// CommentRef preserves both the exact comment identity and its stored bytes.
type CommentRef struct {
	Bead   string `json:"bead"`
	ID     string `json:"comment_id"`
	SHA256 string `json:"comment_sha256"`
}

// AddendumAssessment holds bounded, explicitly supplied conformance claims. The supported door cannot infer these facts from arbitrary prose.
// It appears in the assessor report, or as explicitly Manager-recorded claims
// linked to a retained prose report. The latter is never new assessor output.
type AddendumAssessment struct {
	Original                  CommentRef `json:"original"`
	Head                      string     `json:"head"`
	Base                      string     `json:"base"`
	RunID                     string     `json:"run_id"`
	ManifestPath              string     `json:"manifest_path"`
	ManifestSHA256            string     `json:"manifest_sha256"`
	TranscriptPath            string     `json:"transcript_path"`
	TranscriptSHA256          string     `json:"transcript_sha256"`
	SkillSHA256               string     `json:"skill_sha256"`
	PeerRuleSHA256            string     `json:"peer_rule_sha256"`
	RecoveryRuleSHA256        string     `json:"recovery_rule_sha256"`
	OriginalBindingSHA256     string     `json:"original_binding_sha256"`
	PeerJobID                 string     `json:"peer_job_id"`
	PeerStartEpoch            int64      `json:"peer_start_epoch"`
	Rule                      string     `json:"rule"`
	SoleObjection             bool       `json:"sole_objection"`
	NoFindings                bool       `json:"no_findings"`
	MandatoryCoverageComplete bool       `json:"mandatory_coverage_complete"`
	ValidatorsComplete        bool       `json:"validators_complete"`
	PeerProducedNoReview      bool       `json:"peer_produced_no_review"`
	QuotaFallbackAuthorized   bool       `json:"quota_fallback_authorized"`
	StartPersistedBeforeLocal bool       `json:"start_persisted_before_local"`
}

const AssessmentPrefix = "workflow-review-assessment@v1 "
const WithdrawnPeerSeal = "withdrawn-extra-failed-peer-seal"

// AddendumTranscription is current Manager input linked to retained assessor prose.
type AddendumTranscription struct {
	Source CommentRef         `json:"source"`
	Claims AddendumAssessment `json:"claims"`
}

const ManagerTranscription = "current-manager-transcription"

// AddendumRecord is an append-only block comment. Actor and time describe the
// current recording door, never a retroactive historical seal.
type AddendumRecord struct {
	Representation      string             `json:"representation,omitempty"`
	OriginalAuthor      string             `json:"original_author"`
	OriginalCreatedAt   string             `json:"original_created_at"`
	AssessmentAuthor    string             `json:"assessment_author"`
	AssessmentCreatedAt string             `json:"assessment_created_at"`
	ID                  string             `json:"id"`
	Project             string             `json:"project"`
	Block               string             `json:"block"`
	Step                string             `json:"step"`
	Binding             string             `json:"binding_revision"`
	Assessment          AddendumAssessment `json:"assessment"`
	AssessmentComment   CommentRef         `json:"assessment_comment"`
	PolicyComment       CommentRef         `json:"policy_comment"`
	OriginalRoles       []string           `json:"original_roles"`
	AssessorRoles       []string           `json:"assessor_roles"`
	Actor               string             `json:"actor"`
	RecordedAt          string             `json:"recorded_at"`
}

func (r AddendumRecord) Payload() string { b, _ := json.Marshal(r); return string(b) }
func (r AddendumRecord) Reference() AddendumRef {
	return AddendumRef{ID: r.ID, SHA256: TextDigest(r.Payload())}
}
func (r AddendumRecord) Comment() string { return AddendumPrefix + r.Payload() }

func ParseAssessment(text string) (AddendumAssessment, error) {
	var a AddendumAssessment
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if raw, ok := strings.CutPrefix(line, AssessmentPrefix); ok {
			count++
			if err := json.Unmarshal([]byte(raw), &a); err != nil {
				return a, err
			}
		}
	}
	if count != 1 {
		return a, fmt.Errorf("exactly one explicit independent assessment required")
	}
	if a.Rule != WithdrawnPeerSeal || !a.SoleObjection || !a.NoFindings || !a.MandatoryCoverageComplete || !a.ValidatorsComplete || !a.PeerProducedNoReview || !a.QuotaFallbackAuthorized || !a.StartPersistedBeforeLocal || a.PeerJobID == "" || a.PeerStartEpoch <= 0 || a.RunID == "" {
		return a, fmt.Errorf("assessment does not establish sole withdrawn extra failed-peer seal objection")
	}
	for _, d := range []string{a.ManifestSHA256, a.TranscriptSHA256, a.SkillSHA256, a.PeerRuleSHA256, a.RecoveryRuleSHA256, a.OriginalBindingSHA256, a.Original.SHA256} {
		if err := (AddendumRef{ID: "digest", SHA256: d}).Validate(); err != nil {
			return a, err
		}
	}
	for _, sha := range []string{a.Head, a.Base} {
		b, e := hex.DecodeString(sha)
		if e != nil || len(b) != 20 {
			return a, fmt.Errorf("full head and base required")
		}
	}
	return a, nil
}

// AddendumVerdict leaves every original byte intact and accounts for exactly
// one NOT CLEAN report. Any other same-head NOT CLEAN still refuses, and so does
// anything typed results would refuse: an addendum answers the withdrawn extra
// rule, never a source finding or missing coverage.
func AddendumVerdict(r Review, head, originalText string) error {
	if !hasHeadLine(originalText, head) {
		return fmt.Errorf("original report has wrong head")
	}
	_, bad := verdictLines(originalText)
	if !bad {
		return fmt.Errorf("addendum requires retained original NOT CLEAN")
	}
	seen := 0
	for _, c := range r.Comments {
		clean, notClean := verdictLines(c)
		if c == originalText {
			seen++
			continue
		}
		if (clean || notClean) && !hasHeadLine(c, head) {
			return fmt.Errorf("another review report has a stale or unbound head")
		}
		if notClean {
			return fmt.Errorf("additional unresolved NOT CLEAN report")
		}
	}
	if seen != 1 {
		return fmt.Errorf("exact original report must occur once")
	}
	if typed, err := resultVerdictRetaining(r, head, originalText); typed {
		return err
	}
	return nil
}
