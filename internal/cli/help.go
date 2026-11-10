package cli

import (
	"fmt"
	"io"
)

// helpText is the top-level help screen. Kept inline (not a template) so the
// binary stays self-contained and there is no runtime file lookup.
const helpText = `commander — tmux-orchestrated agent cockpit (Commander / Manager / Operator)

Usage:
  cmdr                              Launch or attach the commander tmux session
  cmdr --project <name>
  cmdr --task <id>
  cmdr root
  cmdr update
  cmdr reload
  cmdr refresh
  cmdr restart [--hard] [--fresh]
  commander [same flags/command]    # alias

  Opening the cockpit (no arguments, --project, --task, root, reload, restart) needs your own
  terminal: it refuses with exit 2 for an agent (COMMANDER_ROLE or COMMANDER_MANAGER_ROLE set)
  or when stdin or stdout is not a terminal.

Commands:
  update                            Rebuild and reinstall cmdr from the Commander repo
  reload                            Update cmdr, then restart the Commander session
  refresh                           Recreate currently open Commander-owned windows for development
  restart [--keep-chats] [--hard] [--fresh]
                                    Reload the cockpit chrome and keep Claude sessions running
                                    (--keep-chats: even chats whose launch command changed);
                                    --hard kills everything and relaunches (sessions resume);
                                    --fresh is --hard with a brand-new Commander chat
  root                              Focus the Commander root Projects view
  app [--web]                       Open the Commander desktop app (starts cmdrd if needed); --web (or a
                                    Linux box with no display) prints the cmdrd-served /app/ link instead
  tab add (--url <U> | --cmd "<sh>") [--title T] [--ws BEAD --project P] [--focus] [--refresh 30s]
  tab rm|focus <n|#id|url> | tab move <n|#id|url> <pos> | tab ls [--json]
                                    Pin PRs, issues, specs, previews in the inspector (right column);
                                    defaults to the window you run it in
  linear set-key | linear status   Save a Linear API key from stdin/hidden prompt, or check its local presence
  browser login [--url <U>] | browser status
                                    Sign the inspector's headless-Chrome profile ($COMMANDER_HOME/browser-profile)
                                    in to Notion, Linear, GitHub, Datadog, Slack (quit Chrome when done);
                                    status shows which are signed in. Signed-in Notion/Linear tabs become pictures
  runtime <role> [claude|codex]     Print or switch the agent CLI a chat runs on (commander, manager-<bead>,
                                    operator-<name>); a running chat restarts on it and reads its history
  runtime report [--days N]         Operators per runtime over N days (default 7): started, finished, died,
                                    respawned, median time to finish, shipped, tokens
  account list [--json]             Claude accounts (plus Codex): login, status, 5h/7d usage and when it was reported
  account add <name>                Create a Claude account (shares settings, skills, plugins, MCP) and print its login line
  account use <role> <name>         Switch a chat to a logged-in account; it restarts there and resumes the same session
  account auto <role> [on|off]      Switch accounts by itself when a usage limit stops a turn (off by default)
  internal-add-project --name <slug> --type <code|knowledge> --path <abs>
                                    Internal project-add entrypoint used from chat
  internal-create-objective --project <slug> --title <text> [--parent <bead-id>] [--workstream [--take-open-steps]]
                                    Internal objective creation entrypoint used from chat/UI
                                    (--workstream: a separate outcome nested under the --parent workstream; --take-open-steps moves the parent's open steps into it)
  internal-track convert --project <slug> --bead <id> --into <workstream bead>
                                    Move a bead under a workstream as a nested workstream (parent edge + kind:workstream)
  internal-migrate-workstreams --project <slug> [--apply]
                                    Label top-level objectives and old tracks kind:workstream; list workstreams with mixed children
  internal-place --project <slug> --bead <id> [--parent <workstream>] [--after <sibling>|--to <rank>]
  internal-place --ordinary --project <slug> --bead <id> --parent <workstream> [--dry-run]
                                    Put a workstream anywhere in the tree, as a drag in the sidebar does
  internal-workstreams              List the Workstreams column's numbered rows as JSON
  internal-move-objective --project <slug> --bead <id> --to <rank>
                                    Move any bead to priority rank N (1 = top): a workstream among workstreams, anything else among its siblings
  internal-spawn-operator --project <slug> --step <id> --title <text> [--prompt <body>] [--branch <b>] [--model <m>] [--runtime claude|codex] [--account <a>]
                                    Create a task under a step and spawn an Operator for it
  internal-operator-provider <operator-role> [--account <a>] [--runtime claude|codex]
                                    Respawn a running Operator on another account/runtime (same bead, branch, worktree)
  internal-needs-you [--bead <id> [--show]]
                                    List open steps waiting on the user (the ⚑ items) as JSON
  internal-resolve-needs --project <slug> --bead <id> --action approve|defer
                                    Answer a ⚑ (approve = done, defer = park) and tell its Manager
  internal-open-workstream --project <slug> --bead <id> [--step <id>] [--focus]
                                    Open (start/resume) a workstream's Manager
  internal-adopt-manager --project <slug> --bead <id> --window <name>
                                    Make a running Manager the Manager of one workstream
  internal-sync-windows             Rename Manager windows to the workstreams' current ranks
  internal-rename-objective --project <slug> --bead <id> --title <text>
                                    Rename an objective
  internal-close-objective --project <slug> --bead <id> [--then-waiting <wait>]... [--then-owner <agent>] [--then-decision <json>] [--then-step <id>]
                                    Close an objective (mark done); refused while a gate on it has not passed, or when it
                                    leaves open work nothing holds (the board would read State missing) unless --then-* names
                                    the next holder, which is written before the close
  internal-gate add --project <slug> --bead <id> --name <n> --check "<cmd or rule>" --verifier auto|reviewer:<login>|user
  internal-gate apply --project <slug> --bead <id> --template spec|pr-ready|reviewed|pr-approved|merged|live|pilot|investigation|outward|spend [--repo o/r --pr N[,N] --reviewers a,b --live-check "<cmd>"]
  internal-gate check --project <slug> --bead <id> [--name <n>]
  internal-gate pass --bead <id> --name <n> --evidence <text> [--by <who>]
  internal-gate list --bead <id> [--json]    |    internal-gate rm --bead <id> --name <n>
  internal-gate link --project <slug> --bead <id> --after|--before <id>
                                    Gates a stage must pass before it closes (check runs auto ones)
  internal-set-objective --project <slug> --bead <id> [--star true|false] [--status open|in_progress]
                                    [--state not-started|in-progress|blocked|needs-user] [--owner <name>|none]
                                    [--note <text>] [--from <role>] [--no-notify]   (needs-user mails Commander)
                                    [--waiting ci:#N|ci:<owner/repo>#N|review:<login>|user|operator:<role>|none] (repeatable;
                                    cmdrd wakes the Manager when CI finishes or a reviewer answers)
                                    [--workstream|--no-workstream]   (make the bead a nested workstream of its parent, or a plain step)
                                    [--max-operators <n>|none] (workstream bead: its Operator cap)
                                    Star or start an objective
  internal-inbox --agent <role> [--unread] [--mark-read] [--since <ts>] [--limit <n>] [--bodies=false]
                                    Read an agent's mail; --unread shows only mail past its read
                                    cursor and --mark-read advances the cursor
  internal-mail-hook --role <role> [--event UserPromptSubmit|Stop]
                                    Claude Code hook: injects unread mail into the turn (never fails)
  internal-resolve-add-project --name <slug>
                                    Internal pathless project-add resolver used from chat

Flags:
  --project, -p  Open the Manager window for a project.
  --task, -t     Open the Operator window for a task.
  --version, -v  Print version and exit.
  --help, -h     Print this help and exit.

Environment:
  COMMANDER_HOME            Override ~/.commander state directory.
  COMMANDER_REPO            Override the commander checkout used for prompt/agent assets.
  COMMANDER_WORKSPACE_ROOT  Override the Commander session's cross-project working directory (default: ~/code).

Architecture:
  Chat runs in native opencode panes. commander renders the surrounding
  chrome columns inside tmux-managed panes and windows.

See share/contracts.md for the verified external CLI shapes commander
invokes (opencode, br, am, docker, tmux, sqlite3, jq).
`

// PrintHelp writes the top-level help text to w.
func PrintHelp(w io.Writer) {
	fmt.Fprint(w, helpText)
}
