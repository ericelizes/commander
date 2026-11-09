# Agent-mail routing: server, not CLI

## Problem

commander coordinates agents through `am` (Agent Mail): it registers a Manager
agent per project and routes mail Commander → Manager → Operator. Originally this
shelled out to the **`am` CLI** (`internal/agentmail` and the agent prompts).

That cannot work on a real deployment. The `am` CLI accesses the mailbox SQLite
DB **directly**, and `am serve-http` — which must be running because opencode's
MCP integration talks to it (`http://127.0.0.1:8765/mcp/`) — holds that DB. With
the server up, every CLI call fails:

```
mailbox mutation refused ... live Agent Mail process still holds the mailbox
database ... supervised restart or operator intervention is required
```

There is no CLI "client mode" (`--url`/`AM_HOST` don't reroute it). This was
known and punted at U1 (`share/contracts.md`: the am integration test "could not
run because the host MCP Agent Mail server holds an exclusive activity lock").

## Decision

Route commander's am operations through the **server's HTTP/MCP API**, not the
CLI. The server is the single owner of the DB; going through it avoids all lock
contention.

- Transport: HTTP `POST` to `/mcp/`, `Authorization: Bearer <token>`,
  `Content-Type: application/json`, body is a JSON-RPC `tools/call`. The response
  is a single JSON object (no SSE, no initialize handshake). Tool output is the
  JSON string in `result.content[0].text`; tool-level failures set
  `result.isError = true` with the message in that same text.
- Token: `AM_HTTP_BEARER_TOKEN` / `HTTP_BEARER_TOKEN`, else `HTTP_BEARER_TOKEN`
  from `~/.config/mcp-agent-mail/config.env`.
- Implementation: `internal/agentmail/http.go` (`ServerClient`). Tools used:
  `ensure_project`, `register_agent`, `send_message`, `fetch_inbox`,
  `list_agents`.

### Naming caveat (load-bearing)

The server **rejects descriptive role names** like `manager-commander`. Agent
names must be randomly generated adjective+noun combinations from the server's
own vocabulary (e.g. `GoldTiger`). So:

- `RegisterAgent` does **not** take a name — it lets the server assign one and
  **returns** it.
- commander persists a **role → am-name** mapping (e.g. "manager of project X =
  GoldTiger") in its own `state.db`, and resolves role → name before sending.

## Rollout (staged)

1. **`ServerClient`** — HTTP/MCP client. *(done; unit + live tested)*
2. **Mapping + activation** — persist role→am-name in `state.db`; activation
   calls `EnsureProject` + `RegisterAgent` and stores the assigned name.
3. **Agent prompts** — `commander.md` / `lead.md` / `operator-*.md` stop calling
   the `am` CLI; they use commander-provided subcommands (or MCP) that resolve
   role → name and send via the server.

## Existing external recipients

`cmdr internal-mail --from manager-fixture --to-existing-agent BlueLake
--subject 'Synthetic handoff' --body 'Synthetic payload' --ack` explicitly
addresses one existing AM identity in the fleet project. It cannot combine
with `--to` or `--interrupt`. Default role routing is unchanged.

Only Managers, Commander and the user may use it: the command refuses an Operator
(any spelling, also one started before its launcher set `COMMANDER_ROLE`), a
`COMMANDER_ROLE` that differs from `--from`, and any other `--from`, all before it
opens the store or the server. This is a policy guard, not a security boundary:
agents share the OS user and can change these variables or call the server directly.

The sender must already have a local role binding. Before sending, the command
reads both identities with server `whois` (`include_recent_commits:false`) under
the fleet's exact project key. Exact names, positive agent/project IDs, active
identities, distinct agent IDs and matching project IDs are required. Missing,
malformed or normalized identities refuse. A recipient colliding with a local
role or AM binding (case insensitive) refuses with a `--to` hint. This route
never ensures projects, registers agents, writes bindings, signals roles or
starts/stops processes. It sends `auto_contact_if_blocked:false`
(`SendOptions.NoAutoContact`, this route only; role mail leaves the server's
default), so a recipient with no approved contact refuses the send with the server's
error (`CONTACT_REQUIRED`) instead of the server approving the link.

Refusals read differently: `no existing agent` (absent or a normalized name),
`retired or deregistered`, `could not read the identity` (any other whois failure,
retry) and `mail server cannot verify existing identities` (no whois tool, or a
reply missing the identity fields).

The server's [whois implementation](https://github.com/Dicklesworthstone/mcp_agent_mail_rust/blob/main/crates/mcp-agent-mail-tools/src/identity.rs)
uses an existing-project lookup and a project-scoped identity read; it flattens
`id`, `name`, and `project_id` into the returned profile. `list_agents` is bounded
and is not used to prove identity absence. Unsupported server versions or
incomplete profiles fail closed. Identity reads and send are separate calls;
this does not provide an atomic identity snapshot or prove recipient receipt.
`sent:true` records a successful send API response. Obtain acknowledgement before
claiming delivery. An agent the server auto-registered from an earlier send
reads as active, so a passing whois does not prove a real agent is behind the name.
Tests: `TestLookupExistingAgent`, `TestMailExistingAgent`,
`TestMailExistingAgentInvalidFlagsBeforeDeps`, `TestMailExistingAgentAllowedCallers`,
`TestExistingAgentMailRouteInPrompts` and eval `TestExistingAgentMailScenario`.

## am DB recovery

If health is red / `degraded_read_only` (writes hit the git archive but not the
sqlite index, causing drift): stop the server, repair, restart.

```
systemctl --user stop agent-mail.service
am doctor repair -y --backup-dir <dir>
systemctl --user start agent-mail.service
```

Verify with the `health_check` tool: `health_level: green`,
`semantic_readiness: ok`, archive and db project counts equal.
