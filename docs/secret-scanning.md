# Secret scanning

Three layers guard the public repo:

1. **GitHub Action** (`.github/workflows/gitleaks.yml`): gitleaks (pinned, `go install`) runs on every push and PR over the history of the pushed commit. Make it required: Settings > Branches > branch protection rule for `main` > "Require status checks" > add `gitleaks`.
2. **`cmdr internal-ship`**: before the ff-merge it runs `gitleaks detect` over the commits the branch adds and refuses on any finding. Without gitleaks on PATH it prints a warning and skips the scan. Install: `brew install gitleaks`, or a release binary from github.com/gitleaks/gitleaks.
3. **GitHub secret scanning with push protection**: Settings > Code security > enable "Secret scanning" and "Push protection". Push protection rejects a push that contains a known token pattern before it lands.

**Rules belong to the tool.** `internal/ship/gitleaks.toml` vendors gitleaks v8.21.2's default rules (MIT), with the entire global allowlist and all per-rule path allowlists removed. It does not extend built-in defaults. The local tool embeds this file; neither main's nor the branch's `.gitleaks.toml` or `.gitleaksignore` changes policy. The Action loads this tool asset from `refs/remotes/origin/main`, or the commit before a push to main. A missing trusted revision or asset fails closed, including a new main; introducing this workflow therefore requires a reviewed bootstrap with the policy already present in trusted history. Both use an empty private source directory with a gitdir pointer and an explicit empty ignore file.

**History and cumulative change.** Both scan the historical range plus one synthetic commit with the tip's tree and the base as its parent. This gives the detector one complete patch for secrets assembled across commits, while still catching secrets added and later deleted. The synthetic commit creates no refs and changes no checkout. The Action uses trusted main as base for branch/PR runs and BEFORE for pushes to main.

**No way to pass without scanning.** Both scans:
- pass `--ignore-gitleaks-allow`, so an inline `gitleaks:allow` comment silences nothing;
- read the commits with `git log --text --no-textconv --no-ext-diff --no-renames --diff-merges=first-parent`, so a file git thinks binary (a NUL byte, a `-diff` attribute), a merge-only change or a diff driver hides nothing;
- build the range from commits (`refs/heads/main` resolved to a SHA; the Action's trusted ref is spelled in full), so a tag named `main` or `origin/main` cannot stand in;
- are red when gitleaks did not complete: gitleaks v8.21.2 logs `ERR failed to scan Git repository` when its git child writes to stderr, then prints "no leaks found" and exits 0. Any `ERR`/`FTL` line, a "partial scan" or a missing "scan completed" line refuses, and a non-zero exit with no findings reads as a failed scan, not as findings.

Limits: a push or PR that edits `.github/workflows/gitleaks.yml` runs its own version of the scan (GitHub runs the workflow from the scanned commit) and `[skip ci]` skips it; closing that needs repository settings (a ruleset or required workflow, CODEOWNERS on `.github/`). Local tampering by the same OS user (`.git/info/attributes`, git config) is outside both scans.

A false positive requires a reviewed change to the tool-owned rules; repository fingerprints and inline allows have no effect. Rotate any real secret that was found; removing it from history does not make it safe.
