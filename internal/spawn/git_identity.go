package spawn

import "strings"

// OperatorGitDomain is the email domain of every Operator's git committer
// identity. .invalid is reserved (RFC 2606): no person's address is under it.
const OperatorGitDomain = "operator.commander.invalid"

// OperatorGitEmail is the committer email the launcher gives an Operator run:
// every commit object it makes (commit, amend, cherry-pick, revert, am, merge,
// rebase) carries it, a ref move (reset, fast-forward, checkout) makes none.
// That header, not any reflog or commit message, says who wrote a commit
// (cmd/cmdr workflowOperatorCommits).
func OperatorGitEmail(agentName string) string {
	return strings.ToLower(agentName) + "@" + OperatorGitDomain
}

// OperatorGitEnv is the launcher's environment for that identity. Only the
// committer is set: the author stays the configured one (the user's), so a
// product repo's author checks and GitHub attribution are unchanged.
func OperatorGitEnv(agentName string) []string {
	return []string{"GIT_COMMITTER_NAME=" + agentName, "GIT_COMMITTER_EMAIL=" + OperatorGitEmail(agentName)}
}
