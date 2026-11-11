# Bounded review conformance addenda

`workflow add-review-addendum` records a current Manager withdrawal of the extra
failed-peer prelaunch seal policy. The original review bead, report ID, exact
report bytes, head, base and manifest remain unchanged. The result is a separate
conformance record, never a replacement CLEAN verdict. Ordinary review checking
without an addendum still uses the existing verdict rules.

The optional proof field is `"addendum":{"id":"<record-id>","sha256":"<payload-sha256>"}`.
Keep `review_bead` pointing to the original review and preserve the existing
`head`, `fix_commits` and `no_findings` semantics. A reference alone grants nothing.
The completion door resolves one exact append-only block comment and rechecks
its integrity, current PR head/base, current writer revision, original and
assessment comments, registration, ancestry, every historical task-run role,
and retained artifacts. Check evidence includes the reference and says
`original NOT CLEAN retained; conformance addendum accepted`.

For an existing typed assessment, the independent assessor posted one single-line
`workflow-review-assessment@v1 <JSON>` attestation in its closed task's report.
The JSON schema is `AddendumAssessment` in `internal/workflowtemplate/addendum.go`.
It binds the exact original comment ID and SHA256, original task/run/head/base,
manifest and transcript paths and SHA256s, original-binding/Skill/peer-rule/
recovery-rule digests, peer job ID and start epoch. It explicitly attests the sole
`withdrawn-extra-failed-peer-seal` objection, no findings, complete mandatory
coverage and validators, zero peer review, authorized quota fallback, and start
persistence before local dispatch. Missing or false assertions refuse. This is
an explicit assessor statement; the command does not infer acceptance by parsing
freeform prose.

For a closed, stopped assessor whose retained report is prose, the current owning
Manager may instead explicitly supply `--evidence-file transcription.json` to the
same command. No new assessor output or session is required. The file contains:

```json
{"source":{"bead":"ASSESSOR","comment_id":"ASSESSMENT_ID","comment_sha256":"<exact-text-sha256>"},"claims":{"original":{"bead":"REVIEW","comment_id":"ORIGINAL_ID","comment_sha256":"<exact-text-sha256>"},"head":"<full-head>","base":"<full-base>"}}
```

`claims` must contain **all** fields of `AddendumAssessment` described above;
this abbreviated example only shows the source relationship. Unknown fields
(including supplied author/role/time fields), mismatched source IDs/digests,
unsupported rules and incomplete claims refuse. The source must be nonempty
prose without a typed assessment line. There is no automatic prose conversion.
The record labels `representation: current-manager-transcription`: its `assessment`
field is the current Manager's typed representation, **not** a new assessor
statement. `actor`/`recorded_at` identify its current recorder, while
`assessment_comment`, `assessment_author`, `assessment_created_at` and
`assessor_roles` preserve the exact original prose source and task-run provenance.
All comment, artifact, role, head and binding checks run at recording and close.
Neither original report is edited; the original review remains NOT CLEAN.

As the registered owning Manager, run:

```
cmdr workflow add-review-addendum --project P --block B --step S \
  --original-comment ORIGINAL_ID \
  --assessment-bead ASSESSOR --assessment-comment ASSESSMENT_ID \
  --policy-bead POLICY_BEAD --policy-comment POLICY_ID
```

The existing policy comment is preserved and linked by exact ID and digest.
Executing this command records the owning Manager's current withdrawal of this
one enumerated extra policy, attributed to the current role and recording time.
There is no backdating and no claim that the policy was never imposed.
The command returns the reference for the proof. Repeating the same validated
request reuses the existing record. It does not close a step or pass a gate.
Operators and other Manager contexts refuse; role or assessor arguments cannot
replace task-run data. `internal-comment` reserves the record prefix.

The resolver reads only the assessor-anchored manifest/transcript and manifest
members. The original report must already anchor the manifest directory and
SHA256. Absolute/parent traversal members, links, oversized files, missing assets
and digest changes refuse. The bounded packet requires all six original local
lenses plus merge/report, matching prompt/binding/launch/terminal hashes and
identities, distinct recorded threads, successful terminals, and a timestamped
peer receipt before local dispatch. The source review must be complete, Ready to
merge, with no findings or testing gaps. Other or later NOT CLEAN reports refuse.
New manifests cannot repair an unanchored old packet. No evidence is executed.

An addendum answers only that withdrawn objection. Typed `workflow-review-result@v1`
results on the review are judged as `reviewVerdict` judges them, at recording and at
close: a blocker or major finding on the head, or incomplete execution or coverage,
refuses. Only the exact retained report is excused from the legacy NOT CLEAN check.

This reuses Commander's **local advisory role/registration/hook trust model**.
Role environment variables and shared local databases are not cryptographic
Manager principals. A malicious same-UID process can falsify environment, edit
records or race files. Digests show retained consistency, not tamper-proof
historical storage. The assessor report (or the Manager's explicit transcription of that retained
report) supplies semantic coverage and sanctioned fallback; hashes cannot prove the quality of that reasoning. No authentication
service, new evidence store, migration, dispatch or reviewer lifecycle is added.

Verification: `TestWorkflowAddendumOrdinaryClose` runs the interaction scenario
in `tests/evals/scenarios/review-addendum.json` against the actual recording and
ordinary completion check doors with private synthetic services. Negative unit
coverage includes missing coverage, source findings, typed findings and typed
incomplete execution or coverage, altered artifacts, stale
identity/head/base/binding, additional NOT CLEAN, generic-prefix forgery and
Operator/foreign Manager misuse, explicit prose transcription, wrong source
ID/digest, unsupported claims and attempted author impersonation. Fixtures contain only invented identities.

On PostgreSQL, new records belong to the current planning Commander, including
scoped urgent conversations. The canonical write door rechecks ownership under
the project lock and stamps recorder plus jurisdiction revision in the existing
comment row. A later handoff retains that recorded authority. Completion still
checks the exact source, artifacts and independent execution identities; the
stamp alone does not pass a review. Historical Manager records keep their
legacy validation path. `TestPostgresPlanReviewAddendumSurvivesCommanderHandoff`
exercises the typed recording CLI and canonical completion service with private
PostgreSQL, HTTP, retained synthetic artifacts and generic execution identities.
