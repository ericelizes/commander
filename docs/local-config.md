# Local integration settings

Keep installation-specific settings outside the repository in
`~/.commander/local.toml` (`$COMMANDER_HOME/local.toml` when overridden).
The file is read beside the daemon's `config.toml`. Start with
[local.toml.example](local.toml.example).

`[integrations.linear] workspace` is the workspace slug from
`https://linear.app/<workspace>/issue/<ticket>`. It accepts letters, digits,
hyphens and underscores, starting with a letter or digit. An absent file,
omitted field or empty workspace leaves ticket IDs visible without links.
Unknown keys, invalid slugs and malformed TOML report a settings error and
leave ticket links disabled. Existing explicit URL tabs still work.

The authenticated `GET /api/settings` response includes `linear_workspace`.
The file is read on each settings request; reload the app after editing it.
No install or daemon restart is needed to change it once this version is running.
The app never writes this file. Runtime, concurrency and user settings continue
to use `config.toml`; the Linear sprint credential lives only in `linear.key`; other credentials remain in vendor login stores.

Generic Linear and Slack inspector adapters are available without any
company-specific configuration. Local settings select a workspace; they do not
log in or grant access.

## Board time zone

Times the board prints (wait and parked lines in the app, `cmdr board`, check-ins) use
`[user] timezone` of `config.toml` (not `local.toml`), an IANA name:

```toml
[user]
timezone = "America/New_York"
```

Unset means the zone cmdrd runs in; an unknown name logs a warning and does the same.
cmdrd reads it at start: restart it after editing.

## Installing on a remote cmdrd

When the desktop app talks to a cmdrd on another machine (an SSH tunnel to a
workstation) or one that cannot show a dialog, the app's Install shows the
confirmation on the app's machine and runs the install there over SSH. Tell it
where, on the app's machine, in `$COMMANDER_HOME/remote-ssh` (one line: a host,
`user@host` or an `~/.ssh/config` alias; env `COMMANDER_REMOTE_SSH`), or give a
suffix appended to the host name cmdrd reports in `remote-ssh-suffix` (for
example `.example.net`; env `COMMANDER_REMOTE_SSH_SUFFIX`). The explicit target
wins. SSH runs with `BatchMode=yes`, so the key must work without a prompt.

## NEEDS YOU tuning (`~/.commander/config.toml`)

Optional; leave the section out for the defaults. `0` switches a rule off.

```toml
[needs]
stale_days = 7   # a card raised longer ago reads out of date
push_count = 3   # after this many unexplained pushes of a wait/park date, the next needs --push-reason
push_days  = 7   # ... or when the date would slide more than this many days from where it started
```

## Linear sprint (read-only)

Run `cmdr linear set-key` on the cmdrd machine and paste the API key at the
hidden prompt, or pipe it on stdin from a credential tool. Never pass it as a
command argument. This writes `$COMMANDER_HOME/linear.key` (default
`~/.commander/linear.key`) atomically with mode 0600. `cmdr linear status` is a
local, read-only check; without a key it prints the exact destination in one
line. It never tests the key against Linear or displays it. No key belongs in
TOML, a bead, mail, logs or a snapshot.

Optional local settings:

```toml
[integrations.linear]
team = "ABC" # omitted or empty: sprint reads stay off
```

cmdrd serves authenticated `GET /api/linear`, separate from the snapshot. It
makes one read-only GraphQL request for the viewer's issues in that team's
active cycle, sharing concurrent requests and caching every result for five
minutes. Nothing polls when the view is unused. Local settings and the key are
read on refresh; key-file presence or mtime changes invalidate the cache on the
next read. Other settings can take up to five minutes. No restart is
needed. The team key is also the ticket prefix board text drops from titles; cmdrd
reads that prefix at start, so restart cmdrd after changing `team` (the app and
`cmdr board` pick it up at once). No matching team or a truncated result (more than 250 issues) fails
closed rather than presenting an incomplete sprint. No active cycle returns
`ok` with an empty list and a null cycle.

The response has `state` (`ok`, `no_key`, `bad_key`, `unreachable`, or `off`),
`cycle` (`name`, `start`, `end`), `issues` (`id`, `title`, `status_name`,
`status_type`, `url`, `has_workstream`), and `fetched_at`. The app resolves
`has_workstream` (initially false) against its snapshot. Failed refreshes keep
the last successful list and timestamp, with `stale_since`; missing keys and
`off` clear the list. Invalid local settings also return `off`. Credentials
with permissions other than 0600, or symlinks, are rejected as `bad_key`.
Commander never changes Linear tickets or statuses.

## Retired owner names

A name the project used for its owner before the neutral `user` (an old
`approver:<name>` label, `needs-<name>`, `owner:<name>`) counts as the user
everywhere approvals are judged. The publishable source holds no real name:
list yours, one per line (`#` comments allowed), in
`$COMMANDER_HOME/legacy-owner-aliases` (default `~/.commander`), or comma
separated in `COMMANDER_LEGACY_OWNER_ALIASES`. No restart needed; the file is
checked about once a second and re-read when it changes. Any name not listed is
a different approver. Keep the list to the names you really used.

Agents (a process with `COMMANDER_ROLE` or `COMMANDER_MANAGER_ROLE` set) ignore
the environment variable: only the file counts for them. Agent role names
(`commander`, `manager`, `operator`, `manager-*`, `operator-*`) are never an
alias. Agents run as your OS user and could still edit the file; the Manager
and Operator guards refuse commands naming it, a speed bump only, not a barrier.
