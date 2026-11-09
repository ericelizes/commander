# Commander TUI redesign spec

Status: proposal, 2026-10-20. Read-only research; no repo files changed.
Layout is fixed by the user: **left** = navigator (0 Commander, WORKSTREAMS, steps of the selected workstream) with a compact FLEET block at the bottom. **Middle** = header bar over the Claude chat. **Right** = a tabbed INSPECTOR, one set of tabs per workstream.
Already live, kept as-is: the 2-row title pane above the chat (`cmdr __column chat-title`, `internal/tui/column_chat_title.go`, driven by `@cmdr_kind` / `@cmdr_title`), and "0 Commander" as its own section above WORKSTREAMS.

---

## 0. What's wrong today, and the root cause of each problem

| # | Symptom (the user's screenshot) | Root cause in the code or config | Fix (section) |
|---|---|---|---|
| 1 | Wallpaper bleeds through and the chat looks patchy (some gray blocks, some dark) | Ghostty `background-opacity = 0.60` (theme *Tokyo Night Storm Glass*) only applies to cells that use the **default** background. Cells with an **explicit** bg (Claude Code's input box, any lipgloss `Background`) are drawn opaque (Ghostty `background-opacity-cells` defaults to false). tmux `window-style` is `default`, so each pane is a mix of glass and opaque cells. | Give every pane an explicit background with tmux `window-style` (§2.1, step S1) |
| 2 | Sidebar is grey on grey and hard to read | Step text uses `dimStyle` = Overlay1 `#7f849c`. `FaintText`, `hintLines` and the empty states add `Faint(true)`, which halves the luminance again, then it is composited over a 60%-opaque wallpaper. Project tags use `FaintText` too. | No `Faint` anywhere. Readable text never drops below Overlay1, and only on a solid bg (§2.1 text tiers, S2) |
| 3 | "⚑ 2 more need you" and the "⚑ 4 need you" footer are barely visible | `⚑` uses Yellow `#f9e2af` with no weight on its text. The footer sits under the tree with nothing to separate it. | The needs-you total moves onto the `0 Commander` row as a Yellow count. Each workstream row shows its own `⚑ n` (S4) |
| 4 | Nothing separates "0 Commander" from the workstreams | Only one blank line, and the section header uses the same accent as everything else | A quiet `COMMANDER` label, then a `WORKSTREAMS ──── 8` rule (§2.2) |
| 5 | The right column is a narrow strip of tiny text | `cmdr-fleet` / `lead-fleet` render about 8 lines in a 59–134-col pane | The right column becomes the Inspector, and FLEET moves to the bottom-left (§2.4, §2.5) |
| 6 | No column titles and no active-pane signal | `pane-border-style fg=#313244`, which is nearly the bg color. Columns have no titles. | Visible idle borders (Surface1), Mauve active border, section labels in each column, hints only in the focused column (§2.6) |
| 7 | Claude's footer ("auto mode on", "✘ Auto-update failed · Run claude doctor") | Claude Code owns this. The red ✘ is a real broken auto-update on this machine. | Leave the mode line alone. Fix the updater (`claude doctor`), or launch with `DISABLE_AUTOUPDATER=1` in `agentrt.ClaudeInteractiveCmd` (S1b) |

---

## 1. Techniques from the best-designed TUIs

Sources: lazygit (`pkg/gui`, gocui `gui.go`, config docs), k9s skins, btop, gitui, yazi `theme.toml` (default dark flavor), zellij tab-bar / compact-bar plugins (`line.rs`, `tab.rs`), superfile themes, posting (`themes.py`, `posting.scss`), harlequin, and Charm's soft-serve, glow, gum, and crush (`internal/ui/styles`, `header.go`, charmtone palette). Hex values and names below come from those files. The behaviour notes come from docs and READMEs. A background research pass was cut off by a cockpit restart, so a few small style details are from memory and marked (unverified).

1. **One focus signal: the border color of the active panel.** *lazygit* (`activeBorderColor: [green, bold]`; inactive panes use the default fg), *k9s* (`frame.border.focusColor`), *gitui*. The rest of the frame never changes, so the eye has exactly one thing to look for. In Commander, tmux draws the borders, so this is `pane-active-border-style`.
2. **Title in the border, numbered for jumping.** *lazygit* labels panels `[1] Status`, `[2] Files` … and *btop* uses superscript hotkeys (`¹cpu ²mem`). The label is the shortcut, so you don't need a legend. In Commander, workstream numbers already are the window numbers (C-a 1…8). Keep the number column big and aligned.
3. **Color roles, not hues: about 6 tokens.** *crush* names roles (`primary` Charple, `secondary` Dolly, `fgBase` Sash → `fgSubtle` Smoke → `fgMostSubtle` Oyster, `bgBase` Pepper → `bgLessVisible` Char → `bgMostVisible` Iron, plus semantic `warning`/`error`/`success`/`info`). *posting* themes are just `primary, secondary, accent, background, surface, panel` (galaxy: `#C45AFF #a684e8 #FF69B4 #0F0F1F #1E1E3F #2D2B55`). Commander already holds all 26 Catppuccin colors and uses about 10 ad hoc. Collapse them to roles (§2.1).
4. **Depth from background tiers, not more borders.** *posting/Textual* (`background` < `surface` < `panel`), *crush* (`bgBase`/`bgLessVisible`/`bgMostVisible`), *VS Code*. Chrome columns sit one tier darker than the work area. This also fixes the translucency patchwork for free, because every pane gets an explicit bg.
5. **Header = identity pill + texture fill + right-aligned meta.** *crush*'s header draws the logo, then a run of `╱` (min 3) in a subtle color, then details (cwd · model · tokens) on the right. Below a width threshold it switches to `compactLogo` and drops details. *zellij* puts the session name in a colored pill. This builds on the live yellow/blue title bar: keep the saturated color only in the pill, so the bar announces who you're talking to without shouting.
6. **Tabs: active = bold, filled or underlined. Inactive = dim, never hidden. Overflow = counted.** *zellij* tab bar: selected ribbon bold on the accent, unselected ribbon dim; separators are arrows (``) or plain; overflow shows `← +3` / `+2 →`. *posting/harlequin* (Textual `Tabs`): underline bar under the active tab. *lazygit* puts tabs in the panel title (`Local branches - Remotes - Tags`). For the Inspector: Textual-style underline tabs with a number prefix (1–9 = hotkey) and `+N` overflow.
7. **Hints only for the focused context, key bright and label dim.** *lazygit*'s bottom options bar changes per focused panel. *k9s* shows a key menu in its header. Charm `bubbles/help` uses `key desc • key desc` with short and full (`?`) modes. In Commander, each column shows its hint line only while it has tmux focus (focus events are already on), so three columns never show three hint bars.
8. **Selection = full-row background band. Unfocused selection = dimmer band or none.** *lazygit* (`selectedLineBgColor: blue`, plus an `inactiveViewSelectedLineBgColor` (unverified name) for unfocused panes), *yazi* (`hovered` reversed). "Where I am" and "where the cursor is" are different things: *yazi* separates the hovered row from the current dir, and *k9s* separates its crumbs from the row cursor. Commander needs both (§2.2).
9. **Breadcrumbs say where you are, every time.** *k9s* crumbs (`pods › nginx-…`), *yazi*'s path header, *zellij*'s session + tab name. In Commander, the title bar names the agent and the sidebar marks the current window with `▌`. Together they answer "which window am I in" from anywhere on screen.
10. **Icons are opt-in, with a full fallback set, and never the only signal.** *lazygit* `gui.nerdFontsVersion: "3"` (empty = off), *superfile* `nerdfont = true|false`, *yazi* icon rules with plain fallbacks, *k9s* `noIcons`. Meaning lives in color plus position plus text. The icon is extra. the user's font is JetBrainsMono Nerd Font, so default to `nerd`, with `unicode` and `ascii` sets (§2.7).
11. **Degrade by priority at narrow widths.** *lazygit* `screenMode` / portrait layout, *zellij* compact-bar, *crush* compact header, *btop* hides boxes that don't fit. Decide the drop order ahead of time: meta, then counts, then labels, then numbers. Truncate titles with `…`, and in the middle for URLs. Never wrap a list row.
12. **Empty states teach the next action.** *lazygit* ("No stashes"), *posting* (empty collection hints), *gum*. Replace `(no operators)` and `(no items)` with one line saying how to fill it (`a add tab · or ask the Manager: cmdr tab add --url …`), or hide the block entirely when its emptiness is the normal case.
13. **No Faint, no reverse video on translucent terminals.** Applies to every app above: SGR 2 (faint) and SGR 7 (reverse) are rendered differently by every terminal, and over a glass bg they turn to mush. Use explicit fg tiers instead. (`onUserStyle` currently uses `Reverse(true)`.)

---

## 2. Proposed design

### 2.1 Palette: Catppuccin, collapsed to roles

Keep `widgets.Palette` (Mocha) as the raw swatches and add a `Theme` of **roles** on top. Every renderer reads roles only. Add a Latte flavor for light terminals. Because S1 gives every pane an explicit background, the colors look the same whatever the terminal theme is. That is what makes the design "light and dark sane". lipgloss/colorprofile already downgrades truecolor to 256 or 16 colors; the 16-color column gives the intended fallback.

| Role | Use | Mocha (dark, default) | Latte (light) | ANSI-16 fallback |
|---|---|---|---|---|
| `BgChat` | chat pane bg (tmux) | `#1e1e2e` Base | `#eff1f5` | default |
| `BgChrome` | sidebar, inspector, title row 2 | `#181825` Mantle | `#e6e9ef` | default |
| `BgRaised` | cursor band, active tab, title bar row 1 | `#313244` Surface0 | `#ccd0da` | 0 / 7 |
| `Rule` | section rules, separators | `#585b70` Surface2 | `#acb0be` | 8 |
| `BorderIdle` | tmux idle border | `#45475a` Surface1 | `#bcc0cc` | 8 |
| `TextPrimary` | titles, current row, numbers that matter | `#cdd6f4` Text | `#4c4f69` | 15 / 0 |
| `TextSecondary` | step text, tab labels, body | `#a6adc8` Subtext0 | `#6c6f85` | 7 |
| `TextMuted` | meta, hint labels, timestamps (min tier for readable text) | `#7f849c` Overlay1 | `#8c8fa1` | 8 |
| `Accent` | focus: active border, `▌`, current number, cursor `›`, ★ | `#cba6f7` Mauve | `#8839ef` | 5 |
| `Attn` | ⚑ needs the user, and nothing else | `#f9e2af` Yellow | `#df8e1d` | 3 |
| `Run` | ● in progress / working | `#89b4fa` Blue | `#1e66f5` | 4 |
| `Ok` | ✓ done, checks pass | `#a6e3a1` Green | `#40a02b` | 2 |
| `Err` | ✗ blocked, failing | `#f38ba8` Red | `#d20f39` | 1 |
| `KindCommander` | title-bar pill | `#cba6f7` Mauve | `#8839ef` | 5 |
| `KindManager` | title-bar pill | `#74c7ec` Sapphire (live today) | `#209fb5` | 6 |
| `KindOperator` | title-bar pill | `#94e2d5` Teal (live today) | `#179299` | 6 |

Rules:
- **Seven chromatic colors, each with one meaning.** Mauve = you / focus. Yellow = needs the user. Blue = running. Green = done. Red = broken. Sapphire and Teal = agent kind. Peach leaves the status vocabulary: today it marks "in progress", and the eye reads orange as a warning.
- **Commander's pill moves from Yellow to Mauve.** Yellow is reserved for ⚑, and a yellow bar over the chat reads as "something needs you" all day. This is a one-line change in `accentFor`.
- Text tiers on Mantle `#181825`: Text ≈ 12:1, Subtext0 ≈ 8.5:1, Overlay1 ≈ 5:1. All pass WCAG AA. Overlay0 (`#6c7086`, ≈ 3.9:1) is for rules only and never for words.
- Project chips (`ProjectChip` fnv hues) stay, but appear in only one place: the meta line of the expanded workstream (`example-app · 3 of 14 done`). They no longer sit as a grey right-aligned tag on every row.

**Backgrounds (tmux, S1):** chrome panes get `window-style bg=#181825` and `window-active-style bg=#181825`. The chat pane gets `bg=#1e1e2e` (set per pane with `set -p`). All cells are now explicit, so Ghostty draws them opaque and the patchwork disappears. The Claude Code gray input box sits on a solid Base instead of on wallpaper. Trade-off: the cockpit loses the glass look. If the user wants glass back, set Ghostty `background-opacity-cells = true` (Ghostty ≥ 1.2), which makes explicit cells translucent too. It stays uniform, but contrast drops again. Recommendation: opaque in the cockpit.

### 2.2 Left column: navigator

```
  COMMANDER                                     ← label: TextMuted, bold, uppercase
▌ 0  Commander                       ⚑ 4  ● 9   ← fleet totals live here (replaces the "⚑ 4 need you" footer)

  WORKSTREAMS ──────────────────────────── 8    ← rule in Rule color, count right
  1  Background jobs rebuild             ○ 3
› 2  Service health report          ⚑ 2  ● 3    ← cursor row: BgRaised band, full width
      example-app · 3 of 14 done                  ← project chip + progress, TextMuted
      ★ Build                                   ← Next: TextPrimary bold, ★ Accent
      ⚑ 2 need you                              ← Attn glyph, TextSecondary words
      ● Commit plan                             ← Run glyph, TextSecondary words
      ○ Go live                                 ← TextMuted glyph, TextSecondary words
      +6 more
  3  Data checks: write them…            ○ 3
  …
  FLEET ───────────────────────────────────     ← pinned to the bottom
  $4.12 today   $31.80 7d   ● 3 ops   ✗ 0
  ▁▂▂▃▅▃▂▁▂▃▅▇▅▃▂▁▁▂▃▅▃  spend 24h               ← sparkline only when width ≥ 44
  ↑↓ move  ⏎ open  e expand  s star  ? keys     ← only while this pane is focused
```

- **Accordion.** Only one workstream shows its steps: the **cursor** row when the sidebar is focused, otherwise the **current** window's workstream. In window 0 with no cursor movement, the list stays collapsed. That is the user's "workstreams + selector + what is selected". Every other workstream is one line. With 8 workstreams this always fits in 45 rows.
- **Row meta replaces the project tag.** A collapsed row ends with `⚑ n` (Attn, only if n>0) and the dominant state (`● n` Run, else `○ n` open). The project name moves to the expanded meta line, or to group headers in `g` grouped mode.
- **Current vs cursor.**
  - *Current window*: `▌` in Accent at column 0, number in Accent bold, title TextPrimary bold.
  - *Cursor*: BgRaised band across the full pane width plus `›` in Accent. Shown **only while the sidebar has focus** (`tea.FocusMsg` / `BlurMsg`, already wired in `Update`).
  - Unfocused, you see only `▌`, so the column reads as a map and not a form.
- **0 Commander** sits under its own `COMMANDER` label, so it is visually a peer of the whole WORKSTREAMS block and not row 0 of it.
- **Next step that needs the user.** No reverse video (`onUserStyle`). It renders as `⚑ ★ Approve pilot scope`, with the ⚑ in Attn and the text TextPrimary bold.

### 2.3 Middle: header bar (builds on the live 2-row title pane)

Row 1, the identity band (BgRaised):
```
 ◆ COMMANDER  Commander ╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱  claude · opus · ~/code
 ◇ MANAGER  Service health report ╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱  example-app · TEAM-090 · opus
 ▸ OPERATOR  U3 ship Python job worker ╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱  feat/u3-worker · sonnet
```
- The pill (` ◆ COMMANDER `) is bold Crust text on the kind color. The title is TextPrimary bold. The `╱` fill is Surface2 (crush's technique). The meta is TextMuted, right-aligned.
- Row 2, the live status line (BgChrome). This replaces the current empty filler row:
  - Commander: `⚑ 4 need you   ● 9 in progress   3 operators running   $4.12 today`
  - Manager: `★ next  Build   ⚑ 2 need you   ● 3 in progress   ✓ 3 / 14`
  It reuses `CommanderLiveData` and the workstream rows the sidebar already computes, and refreshes on `columnTick`.
- **Narrow (< 100 cols):** pill and title only. The fill shrinks to at least 3 `╱`, and meta drops before the title truncates. Row 2 compacts to `★ Build  ⚑ 2  ● 3  ✓ 3/14`.
- Keep the 2 rows. The tmux border under the pane is the separator. The bar takes no keys and never takes focus. That is already true; keep it.

### 2.4 Right column: Inspector

Each window's right pane runs `cmdr __column inspector --ws <bead> --project <p>`, or no flags in window 0. Each window owns its own process, so "switching windows switches tabs" with no extra work.

```
  INSPECTOR                                                         5 tabs
  1 ⎇ PR #4081   2 ◇ TEAM-090   3 ▤ Spec   4 ◷ Datadog   5 ◎ localhost:5173   +
 ━━━━━━━━━━━━━━──────────────────────────────────────────────────────────────── ← Accent underline = active
  ⎇ PR #4081  OPEN                                   github.com/…/example-app/pull/4081
  U3: ship Python job worker unit
  feat/u3-worker → main · +412 −38 · 9 files · updated 12m ago
  CHECKS  ✓ 11  ✗ 1  ○ 2            REVIEW  ⚑ awaiting you  ✓ rev
  …body rendered as markdown…
  1-9 tab  [ ] cycle  o open  y copy url  r refresh  x close     ← focused only
```
- **Tab bar** (techniques 6 and 11): `n icon label`. The active tab is TextPrimary bold with an Accent `━` underline. Inactive tabs are TextSecondary. A tab whose content needs the user (PR awaiting his review, a failing check) gets a tiny Attn `•`. Overflow: if tabs don't fit, they collapse to `n icon` and then to `‹ +k` / `+k ›`. Below 50 cols, labels drop and the active tab's label moves to the card header.
- **Card header**: kind icon, title, state pill (OPEN/MERGED/DONE in Ok/Run/Muted), URL TextMuted right-aligned and truncated in the middle, plus "updated Xm ago". `r` refreshes. Results are cached in the store so a window switch renders instantly.
- **Empty state** (window with no tabs): centered, TextMuted:
  `No tabs yet.  a  add a URL   ·   Managers: cmdr tab add --url <url>`

#### How to show "a screen at a URL" in a tmux pane

| Approach | Works in tmux + Ghostty? | Fidelity | Cost | Verdict |
|---|---|---|---|---|
| **Adapter → structured card** (GitHub PR via `gh pr view --json`, Linear issue via GraphQL, Notion page via API → markdown) | Yes. Plain text. | High for these sources: you see state, checks, reviewers and body, not the chrome | One small adapter per source | **v1 core** |
| **Generic page → readable markdown** (fetch, readability extraction, HTML→Markdown, glamour; same idea as glow) | Yes | Good for docs/specs. Useless for JS apps and dashboards | 2 Go deps (`go-shiori/go-readability`, `JohannesKaufmann/html-to-markdown`) plus `charmbracelet/glamour` | **v1** fallback for any `http(s)` |
| **Command tab** (`gh pr checks 4081 --watch`, `curl -s localhost:5173/health`, any CLI) re-run every N seconds, with ANSI kept | Yes | Whatever the CLI prints | ~0. Uses the same viewport | **v1** (`--cmd`) |
| **Link card + `o` opens in browser** (`open <url>`) | Yes | None in-pane, but one keystroke to the real thing | ~0 | **v1** for Datadog, Grafana and anything authenticated or JS-heavy |
| **Live terminal tab**: a real program (lazygit, k9s, `carbonyl` = Chromium in the terminal) in a hidden tmux pane, swapped into the inspector slot with `swap-pane` | Yes. tmux owns it. | Full interactivity | Pane bookkeeping. carbonyl is heavy and needs auth cookies. | **v2** |
| **Screenshot image**: headless Chrome (chromedp) → PNG → kitty graphics protocol | Ghostty supports kitty graphics but not sixel. Through tmux it needs `set -g allow-passthrough on` **and** kitty's Unicode-placeholder mode (what yazi/timg use under tmux), or images drift and smear on redraw. | Pixel-true dashboards | Auth (Datadog/Grafana sessions), refresh cost, flaky under pane resizes | **v2**, dashboards only. Prefer Grafana's `/render` and Datadog's graph-snapshot APIs over a full browser. |

**v1 (ship it):** tabs of kind `url` with an auto-picked renderer (`github-pr`, `linear`, `notion`, `web`, `link`), tabs of kind `cmd`, and `o` / `y` everywhere.
**v2:** kind `term` (swap-pane live programs) and kind `image` (kitty Unicode placeholders) for dashboards.

#### Data model (migration `0005_inspector_tabs.sql`, same keying as `workstream_ranks` / `workstream_managers`)

```sql
CREATE TABLE inspector_tabs (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL,            -- 0 for the Commander window
  workstream_bead_id TEXT NOT NULL,       -- '' for the Commander window
  position INTEGER NOT NULL,
  title TEXT NOT NULL,
  kind TEXT NOT NULL,                     -- url | cmd | (v2) term | image
  target TEXT NOT NULL,                   -- URL, shell command, or path
  renderer TEXT NOT NULL DEFAULT '',      -- '' = auto-detect from target
  refresh_secs INTEGER NOT NULL DEFAULT 60,
  added_by TEXT NOT NULL,                 -- 'user' | am role e.g. manager-<bead>
  created_at INTEGER NOT NULL,
  UNIQUE (project_id, workstream_bead_id, target)
);
CREATE TABLE inspector_state (            -- which tab is showing, per window
  project_id INTEGER NOT NULL, workstream_bead_id TEXT NOT NULL,
  active_tab_id INTEGER, cache_json TEXT, cached_at INTEGER,
  PRIMARY KEY (project_id, workstream_bead_id)
);
PRAGMA user_version = 5;
```

#### Commands (Managers call these; the user can too)

```
cmdr tab add  (--url U | --cmd "…") [--title T] [--ws BEAD --project P] [--focus] [--refresh 30s]
cmdr tab rm   <id|n|url>            [--ws … --project …]
cmdr tab ls   [--json]              [--ws … --project …]
cmdr tab focus <id|n>               [--ws … --project …]
cmdr tab move <id> <pos>
```
- With no `--ws/--project`, the target defaults to the caller's window. The command reads `@cmdr_ws` and the project from `$TMUX_PANE`, so a Manager just runs `cmdr tab add --url https://github.com/…/pull/4081 --focus`.
- `add` is idempotent on `target` (UNIQUE). If the tab already exists, `--focus` just switches to it.
- Add one paragraph to `lead.md` (the Manager role prompt): *"When you open a PR, file or pick up a Linear issue, write a spec, or start a local preview for this workstream, run `cmdr tab add --url … --focus` so it shows in the user's inspector. Remove tabs that are merged or closed."*
- `__column inspector` re-reads the table on `columnTick` (5s), so a Manager's add shows up within seconds with no IPC.

### 2.5 FLEET block (moved from the right column)

These are the same numbers as `widgets.Metrics` (24h/7d cost, active/blocked, idle), as a 2–3 line block pinned to the bottom of the left column. The `OPERATORS` list goes away from the default view. Running operators already appear as `●` on their workstreams. Pressing `f` in the sidebar can open a full fleet list in the inspector as a built-in tab (`renderer = fleet`), so no information is lost.

### 2.6 Active window and active pane

- **Which window:** `▌` on the current row in the sidebar, plus the title-bar pill. Both are visible in every window.
- **Which pane:** tmux `pane-active-border-style fg=#cba6f7`, `pane-border-style fg=#45475a`, `pane-border-lines single`. In-app, the focused column's section label turns Accent and its hint line appears. Unfocused columns show no hints and no cursor band.
- `pane-border-status` stays **off**. The in-app labels and the title pane carry the names, so no rows are spent on border titles.

### 2.7 Glyphs

| Meaning | nerd (default; JetBrainsMono NF) | unicode | ascii | Color |
|---|---|---|---|---|
| current window | `▌` | `▌` | `>` | Accent |
| cursor | `›` | `›` | `>` | Accent (+ BgRaised band) |
| next step | `★` | `★` | `*` | Accent |
| in progress | `●` | `●` | `o` | Run |
| not started | `○` | `○` (replaces the too-faint `·`) | `-` | TextMuted |
| needs the user | `⚑` | `⚑` | `!` | Attn |
| blocked | `✗` (was `!`, which clashed with ascii needs-the user) | `✗` | `x` | Err |
| done | `✓` | `✓` | `+` | Ok |
| Commander / Manager / Operator | `◆` / `◇` / `▸` | same | `#` / `@` / `>` | kind color |
| tab: PR / issue / doc / dashboard / local / cmd / web | `` / `` / `󰈙` / `󰄨` / `󰖟` / `` / `󰖟` | `⎇` / `◇` / `▤` / `◷` / `◎` / `$` / `◌` | `PR` `IS` `DOC` `DB` `LH` `$` `WWW` | TextSecondary |

Picked by `CMDR_ICONS=nerd|unicode|ascii` (or `[ui] icons =` in `~/.commander/config.toml`). Default: `unicode` for the status markers, because they never change width, and `nerd` only for tab icons. All markers are single-cell in Ghostty. `★` and `⚑` are East-Asian-ambiguous, so keep `lipgloss.Width` as the only width source, as `tree.go` already does.

### 2.8 Key-hint footers

Format: `key label  key label` with the key in TextPrimary bold, the label in TextMuted, and **two spaces** between pairs (no ` · ` dots: less ink, same grouping). One line, shown only while focused. `?` expands to the full list (the existing `hintLines(width, full)`) wrapped to width. `widgets.Keybind` drops its Surface2 key-cap background, because filled caps on every hint are the loudest thing in a quiet column.

### 2.9 Widths

These replace `-p 25` / `-p 33` in `layoutThreeColumns` with absolute sizes, recomputed on a `client-resized` hook:

```
left  = clamp(round(W * 0.13), 36, 56)
right = min(clamp(round(W * 0.36), 42, 160), W - left - 2 - 80)   // chat keeps ≥ 80 cols
chat  = W - left - right - 2
```
426 → 54 | 216 | 154.  160 → 36 | 80 | 42.  Below 140 cols, the inspector auto-zooms out of the way (`C-a i` toggles it).

Today, window 0 is 60 | 305 | 59 and workstream windows are 136 | 154 | 134 (only 154 cols for chat). The new rule makes every window identical.

---

## 3. Mockups

Legend: `▌` = current window (Accent bar, bold). `›` = cursor, drawn only when the sidebar is focused, as a full-width BgRaised band. `━` under a tab = active tab. Row 1 of the title pane is the BgRaised identity band with a kind-colored pill. Row 2 is the status line. The `─` under the title pane and the `│` separators are tmux borders: Mauve on the focused pane, Surface1 elsewhere. The chat body is Claude Code's own rendering, sketched. Metric numbers are illustrative.

### Window 0 (Commander), 426×72 — sidebar unfocused, cursor parked on 2 so its steps show

```text
                                                      │ ◆ COMMANDER  Commander ╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱ claude · opus · ~/code │  INSPECTOR                                                                                                                                       3 tabs
  COMMANDER                                           │   ⚑ 4 need you   ● 9 in progress   3 operators running   $4.12 today                                                                                                                                                   │  1 ⎇ PRs to review   2 ◇ example-app board   3 ▤ Wiki      +
▌ 0  Commander                               ⚑ 4  ● 9 │────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────│─━━━━━━━━━━━━━━━━━━━──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
                                                      │                                                                                                                                                                                                                        │
  WORKSTREAMS ───────────────────────────────────── 8 │ ❯ what needs me today?                                                                                                                                                                                                 │  PRs to review                                                                    gh search prs --review-requested=@me --state=open  ·  updated 40s ago
  1  Background jobs rebuild (TEAM-101)           ○ 3 │                                                                                                                                                                                                                        │
› 2  Service health report                   ⚑ 2  ● 3 │ ⏺ Four steps are waiting on you. The two that unblock the most work:                                                                                                                                                   │  #4080  example-app            Queue worker scaffolding                    ✓ 14 checks   ✓ 1 approval    2h
      example-app · 3 of 14 done                        │   1. Service health report — approve the pilot scope (alerts rollout)  .                                                                                                                                               │  #4079  example-app            Job schema v2                               ✗ 1 failing   — no review     5h
      ★ Build                                         │   2. Trim help queue — closing old tickets needs team sign-off.                                                                                                                                                        │  #4014  example-automation   Retry stuck export job                      ✓ 9 checks    ✓ 2 approvals   1d
      ⚑ 2 need you                                    │                                                                                                                                                                                                                        │
      ● Commit plan                                   │ ⋮  (Claude Code transcript, rendered by Claude — not ours)                                                                                                                                                             │
      ● U3 ship Python job worker unit                │                                                                                                                                                                                                                        │
      ○ Go live                                       │                                                                                                                                                                                                                        │
      ○ First pilot: move the alerts over             │                                                                                                                                                                                                                        │
      +6 more                                         │                                                                                                                                                                                                                        │
  3  Data checks: write them + run them           ○ 3 │                                                                                                                                                                                                                        │
  4  Prompt experiments                           ○ 3 │                                                                                                                                                                                                                        │
  5  Developer setup: fix the dev flow            ○ 2 │                                                                                                                                                                                                                        │
  6  Keep nightly jobs healthy                    ● 2 │                                                                                                                                                                                                                        │
  7  Trim help queue (TEAM-102)              ⚑ 1  ● 1 │                                                                                                                                                                                                                        │
  8  Commander                               ⚑ 1  ● 4 │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
  FLEET ───────────────────────────────────────────── │                                                                                                                                                                                                                        │
  $4.12 today   $31.80 7d   ● 3 ops   ✗ 0             │────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────│
  ▁▂▂▃▅▃▂▁▂▃▅▇▅▃▂▁▁▂▃▅▃▂▁▂▃▅▇▅▃  spend 24h            │ ❯                                                                                                                                                                                                                      │
                                                      │────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────│
  ↑↓ move  ⏎ open  e expand  s star  ? keys           │   ⏵⏵ auto mode on (shift+tab to cycle)                                                                                                                                                                                 │  1-9 tab  [ ] cycle  o open  y copy  r refresh  x close
```

### Workstream window 2 (Service health report), 426×72

```text
                                                      │ ◇ MANAGER  Service health report ╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱ example-app · TEAM-090 · opus │  INSPECTOR                                                                                                                                       5 tabs
  COMMANDER                                           │   ★ next  Build      ⚑ 2 need you      ● 3 in progress      ✓ 3 / 14                                                                                                                                                   │  1 ⎇ PR #4081   2 ◇ TEAM-090   3 ▤ Spec   4 ◷ Datadog   5 ◎ localhost:5173   +
  0  Commander                               ⚑ 4  ● 9 │────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────│─━━━━━━━━━━━━━━───────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
                                                      │                                                                                                                                                                                                                        │
  WORKSTREAMS ───────────────────────────────────── 8 │ ❯ status on the worker unit?                                                                                                                                                                                           │  ⎇ PR #4081  OPEN                                                                                                      github.com/…/example-app/pull/4081
  1  Background jobs rebuild (TEAM-101)           ○ 3 │                                                                                                                                                                                                                        │  U3: ship Python job worker unit
▌ 2  Service health report                   ⚑ 2  ● 3 │ ⏺ U3 is up as PR #4081 — 11 checks green, 1 failing (integration / queue-worker).                                                                                                                                   │  feat/u3-worker → main  ·  +412 −38  ·  9 files  ·  updated 12m ago
      example-app · 3 of 14 done                        │   I opened it in the inspector (tab 1). The failure is a missing QUEUE_ADDRESS in CI.                                                                                                                               │
      ★ Build                                         │                                                                                                                                                                                                                        │  CHECKS   ✓ 11   ✗ 1   ○ 2                         REVIEW   ⚑ awaiting you   ✓ rev
      ⚑ 2 need you                                    │ ⋮  (Claude Code transcript, rendered by Claude — not ours)                                                                                                                                                             │  ✗ integration / queue-worker      2m 14s   QUEUE_ADDRESS not set
      ● Commit plan                                   │                                                                                                                                                                                                                        │
      ● U3 ship Python job worker unit                │                                                                                                                                                                                                                        │  DESCRIPTION
      ○ Go live                                       │                                                                                                                                                                                                                        │  Adds the Python job worker as a standalone unit so per-app jobs run outside the web process.
      ○ First pilot: move the alerts over             │                                                                                                                                                                                                                        │  Wires the queue client config from env; adds a smoke workflow and a health endpoint.
      +6 more                                         │                                                                                                                                                                                                                        │
  3  Data checks: write them + run them           ○ 3 │                                                                                                                                                                                                                        │  ▸ 3 comments   ▸ 9 files
  4  Prompt experiments                           ○ 3 │                                                                                                                                                                                                                        │
  5  Developer setup: fix the dev flow            ○ 2 │                                                                                                                                                                                                                        │
  6  Keep nightly jobs healthy                    ● 2 │                                                                                                                                                                                                                        │
  7  Trim help queue (TEAM-102)              ⚑ 1  ● 1 │                                                                                                                                                                                                                        │
  8  Commander                               ⚑ 1  ● 4 │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
                                                      │                                                                                                                                                                                                                        │
  FLEET ───────────────────────────────────────────── │                                                                                                                                                                                                                        │
  $4.12 today   $31.80 7d   ● 3 ops   ✗ 0             │────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────│
  ▁▂▂▃▅▃▂▁▂▃▅▇▅▃▂▁▁▂▃▅▃▂▁▂▃▅▇▅▃  spend 24h            │ ❯                                                                                                                                                                                                                      │
                                                      │────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────│
  ↑↓ move  ⏎ open  e expand  s star  ? keys           │   ⏵⏵ auto mode on (shift+tab to cycle)                                                                                                                                                                                 │  1-9 tab  [ ] cycle  o open  y copy  r refresh  x close
```

### Window 0 (Commander), 160×45 — compact sidebar, numbered-icon tabs

```text
                                    │ ◆ COMMANDER ╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱ opus │  INSPECTOR                       3 tabs
  COMMANDER                         │   ⚑ 4 need you  ● 9 in progress                                                │  1 ⎇   2 ◇   3 ▤   +
▌ 0  Commander                  ⚑ 4 │────────────────────────────────────────────────────────────────────────────────│─━━━━━────────────────────────────────────
                                    │                                                                                │
  WORKSTREAMS ─────────────────── 8 │ ❯ what needs me today?                                                         │  PRs to review
  1  Background jobs rebuild…       │                                                                                │  gh search prs · 40s ago
› 2  Service health report      ⚑ 2 │ ⏺ Four steps are waiting on you. The two that unblock the most work:           │
      example-app · 3 of 14 done      │   1. Service health report — approve the pilot scope (alerts rollout)  .       │  #4080 Queue worker…     ✓
      ★ Build                       │   2. Trim help queue — closing old tickets needs team sign-off.                │  #4079 Job schema v2    ✗
      ⚑ 2 need you                  │                                                                                │  #4014 Ops retry fix    ✓
      ● Commit plan                 │ ⋮  (Claude Code transcript, rendered by Claude — not ours)                     │
      ● U3 ship Python job worker u…│                                                                                │
      ○ Go live                     │                                                                                │
  3  Data checks: write them …      │                                                                                │
  4  Prompt experiments             │                                                                                │
  5  Developer setup: fix the dev…  │                                                                                │
  6  Keep nightly jobs healthy…     │                                                                                │
  7  Trim help queue (TEAM-1…   ⚑ 1 │                                                                                │
  8  Commander                  ⚑ 1 │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
  FLEET ─────────────────────────── │────────────────────────────────────────────────────────────────────────────────│
  $4.12 today · ● 3 ops · ✗ 0       │ ❯                                                                              │
                                    │────────────────────────────────────────────────────────────────────────────────│
  ↑↓ move  ⏎ open  ? keys           │   ⏵⏵ auto mode on (shift+tab to cycle)                                         │  1-9 tab  o open  ? keys
```

### Workstream window 2, 160×45

```text
                                    │ ◇ MANAGER  Service health report ╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱ │  INSPECTOR                       5 tabs
  COMMANDER                         │   ★ Build  ⚑ 2  ● 3  ✓ 3/14                                                    │  1 ⎇   2 ◇   3 ▤   4 ◷   +1   +
  0  Commander                  ⚑ 4 │────────────────────────────────────────────────────────────────────────────────│─━━━━━────────────────────────────────────
                                    │                                                                                │
  WORKSTREAMS ─────────────────── 8 │ ❯ status on the worker unit?                                                   │  ⎇ #4081  OPEN
  1  Background jobs rebuild…       │                                                                                │  U3 Python job worker unit
▌ 2  Service health report          │ ⏺ U3 is up as PR #4081 — 11 checks green, 1 failing (integration / queue-worker│  +412 −38 · 9 files · 12m
      example-app · 3 of 14 done      │   I opened it in the inspector (tab 1). The failure is a missing env setting.│
      ★ Build                       │                                                                                │  ✓ 11  ✗ 1  ○ 2
      ⚑ 2 need you                  │ ⋮  (Claude Code transcript, rendered by Claude — not ours)                     │  ✗ integration/queue-worker
      ● Commit plan                 │                                                                                │
      ● U3 ship Python job worker u…│                                                                                │  REVIEW  ⚑ awaiting you
      ○ Go live                     │                                                                                │
  3  Data checks: write them …      │                                                                                │
  4  Prompt experiments             │                                                                                │
  5  Developer setup: fix the dev…  │                                                                                │
  6  Keep nightly jobs healthy…     │                                                                                │
  7  Trim help queue (TEAM-1…   ⚑ 1 │                                                                                │
  8  Commander                  ⚑ 1 │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
                                    │                                                                                │
  FLEET ─────────────────────────── │────────────────────────────────────────────────────────────────────────────────│
  $4.12 today · ● 3 ops · ✗ 0       │ ❯                                                                              │
                                    │────────────────────────────────────────────────────────────────────────────────│
  ↑↓ move  ⏎ open  ? keys           │   ⏵⏵ auto mode on (shift+tab to cycle)                                         │  1-9 tab  o open  ? keys
```

---

## 4. The extra polish (planned, not cut)

the user's bar is "the best possible UI", so all of these are in scope. Each has a plan and a step in §5.

- **Motion with restraint.**
  - Running work gets a spinner instead of a static `●`: `bubbles/spinner` `MiniDot` (`⠋⠙⠹…`) in Run color, used only in the title-bar status line and on the current workstream's in-progress steps. Elsewhere `●` stays static, so the screen isn't a casino.
  - A new `⚑` pulses for about 2s (Attn ↔ TextPrimary, 4 frames at 150ms via `tea.Tick`) the first time it appears, then settles.
  - The ascii set disables all motion. So does `CMDR_MOTION=off`.
- **Gradient wordmark in the header.** Crush renders its logo with `ApplyBoldForegroundGrad` (per-rune color interpolation). Do the same for `COMMANDER` in window 0's pill: Mauve → Pink, `#cba6f7` → `#f5c2e7`. This is 20 lines with `lucasb-eyer/go-colorful`, which lipgloss already pulls in transitively. Managers and Operators keep flat pills, so window 0 is unmistakable.
- **Loading skeletons, not blank panes.** While the first `loadLiveCmd` / inspector fetch is in flight:
  - Draw placeholder bars `▁▁▁▁▁▁▁▁` in Surface0 at the real row positions: 8 workstream rows, a card header and 3 body lines.
  - Run a one-cell shimmer (Surface1) across them every 120ms.
  - Show it after 150ms, never on cache hits. The inspector always renders the cached card first, with `refreshing…` in TextMuted next to "updated Xm ago".
- **Error states with a way out.** Replace `Commander state unavailable` + a faint error with a bordered card (rounded border in Err), a one-line cause, the full error in TextMuted, and the fix key: `r retry  ·  cmdr doctor`.
- **Inspector images (v2, full plan).**
  1. `tmux set -g allow-passthrough on`, set by `ensureCommanderSessionChrome`.
  2. `renderer=image` tabs fetch a PNG. Use Grafana `/render/d-solo/...?width&height` or the Datadog graph snapshot API. Only fall back to headless Chrome via `chromedp` when there is no API, using a persistent profile dir under `~/.commander/chrome` that the user logs into once.
  3. Upload with the kitty graphics protocol (`a=T,U=1` virtual placement), then print Unicode placeholder cells (U+10EEEE with diacritics encoding the row and column). tmux treats them as text, so the image moves, clips and scrolls with the pane. This is the yazi/timg approach.
  4. On `WindowSizeMsg`, re-request at the new cell size × cell pixel size (query `CSI 16 t` once).
  5. Fallback: if passthrough or kitty support is missing, render the PNG as half-block `▀` art (24-bit fg/bg per cell; `charmbracelet/x/mosaic` or ~40 lines by hand). It's coarse but always works and still beats a link.
- **Live program tabs (v2).**
  - `kind=term` tabs run in a parked window (`commander:_tabs`).
  - On activation, `swap-pane -s _tabs.<pane> -t <inspector>`, which swaps the Bubble Tea inspector out and the program in. `C-a [` / `C-a ]` cycle tabs (tmux bindings that call `cmdr tab focus`).
  - The tab bar then has to live in the tmux border, so turn on `pane-border-status top` **for the inspector pane only** (`set -p`), with a `pane-border-format` that renders the same tab strip. The strip comes from `cmdr tab ls --strip`, cached into a pane option `@cmdr_tabs` on each change.
- **Custom chat header.** The title pane is ours, so it can go further than text:
  - A thin context-usage meter at the right end of row 2 (`▰▰▰▰▱▱ 61%`, from the Claude session JSONL that `agentrt` already parses).
  - Model, and cost this session.
  - A blinking (motion-gated) `●` while Claude is generating.

---

## 5. Implementation plan (small, shippable steps)

Each step is one PR. Each must pass `go test ./...` plus a `scripts/cockpit-test.sh text` capture, per CLAUDE.md. Visual tests: extend the existing `teatest`/golden setup for every changed `View()` at widths 36 / 54 and heights 45 / 72.

**S1. Solid backgrounds and visible framing** (`internal/layout/layout.go`)
- Per window (in `layoutThreeColumns`, see the note below), add:
  ```go
  {"set-option", "-w", "-t", CommanderSessionName + ":", "pane-border-style", "fg=#45475a"},
  {"set-option", "-w", "-t", CommanderSessionName + ":", "pane-active-border-style", "fg=#cba6f7"},
  {"set-option", "-w", "-t", CommanderSessionName + ":", "pane-border-lines", "single"},
  {"set-option", "-w", "-t", CommanderSessionName + ":", "window-style", "bg=#181825"},
  {"set-option", "-w", "-t", CommanderSessionName + ":", "window-active-style", "bg=#181825"},
  ```
  Note: `-w -t commander:` only hits the current window, so either set these with `-g` (they then override `~/.tmux.conf` globally) or, preferably, set them per window inside `layoutThreeColumns` with `-w -t <chatPaneID>`. That way every window, including ones created later, gets them.
- In `layoutThreeColumns`, after tagging the chat pane:
  ```go
  {"set-option", "-p", "-t", chatPaneID, "window-style", "bg=#1e1e2e"}
  {"set-option", "-p", "-t", chatPaneID, "window-active-style", "bg=#1e1e2e"}
  ```
- Also set `@cmdr_col` = `nav` / `inspector` on the two split panes. `split-window -P -F '#{pane_id}'` already returns their ids, which are discarded today.
- `soft_restart.go` / `Refresh` must re-apply these to existing windows.
- Tests: `layout_test.go` runner expectations for the new `set-option` calls.

**S1b. Claude footer noise** (`internal/agentrt/claude.go: ClaudeInteractiveCmd`)
- First run `claude doctor` on this machine. If auto-update can't work here, prefix `DISABLE_AUTOUPDATER=1 ` in the interactive command. Leave "auto mode on" alone; that's Claude's mode line.

**S2. Theme roles and no Faint** (`internal/tui/widgets/theme.go`)
- Add `type Theme struct{ BgChat, BgChrome, BgRaised, Rule, BorderIdle, TextPrimary, TextSecondary, TextMuted, Accent, Attn, Run, Ok, Err, KindCommander, KindManager, KindOperator lipgloss.Color }`, with `Mocha` and `Latte` values from §2.1 and `var T = Mocha` selected from `[ui] flavor` in config.toml.
- Redefine `FaintText` = `Foreground(T.TextMuted)` (drop `.Faint(true)`). `DimText` = `T.TextSecondary`. `HeaderStyle` = bold `T.TextMuted`; the Accent version applies only when focused, via a new `SectionLabel(label string, focused bool, count string, width int) string` that renders `LABEL ─── n`. `AccentBar` stays for the current-row `▌`.
- `StatusColor`: `working/executing` → Run, `needs-input` → Attn, `blocked` → Err, `done` → Ok, `idle` → TextMuted. Drop Peach.
- `Keybind(key, label)`: bold TextPrimary key, TextMuted label, no background. Add `HintLine(width int, pairs ...[2]string)`, joined with two spaces and truncated by dropping trailing pairs, never mid-pair.
- Grep and replace every `.Faint(true)` in `internal/tui` (`column_objectives.go` `hintLines` / `renderProjectHierarchy` / `renderNoObjectives` / `renderStatus`, `column_fleet.go`, `column_lead_fleet.go`, `column_operator_side.go`).

**S3. Markers, rows, current vs cursor** (`workstream_rows.go`, `widgets/tree.go`)
- `marker(state)`:
  - not started `○` TextMuted
  - in progress `●` Run
  - needs the user `⚑` Attn
  - blocked `✗` Err
  - done `✓` Ok
  - all Bold
  Glyphs come from a new `widgets.Glyphs` set picked by `CMDR_ICONS`.
- Styles: `dimStyle` → TextSecondary (not Overlay1), `normalStyle` → TextPrimary, `brightStyle` → TextPrimary bold. Delete `onUserStyle` (reverse); a needs-the user Next renders as `⚑ ★ title` in bold.
- `Tree`: add `Focused bool`, set from `tea.FocusMsg` / `BlurMsg` (already handled in `ObjectivesColumn.Update`).
- In `View` / `columnarRow`:
  - `Current` → `▌` Accent in column 0 + Accent bold number + TextPrimary bold title.
  - Selected and `Focused` → wrap the row in `lipgloss.NewStyle().Background(T.BgRaised).Width(t.width)`. Re-render the inner segments with the same bg, because lipgloss resets the bg after each styled segment. Build the row from styles that each carry `.Background(T.BgRaised)` when selected.
  - Selected and not focused → no band, no `›`.
- Tests: `tree_test.go`, `workstream_rows_test.go`, `sidebar_test.go` (focus/blur toggles the band).

**S4. Navigator structure** (`column_objectives.go: rebuildTree`, `View`, `workstreamRows`)
- Before the `commanderRowID` node, insert a header node `COMMANDER` (`Header: true, Skip: true`). The WORKSTREAMS header gets `RightTag: fmt.Sprint(len(order))` and the rule rendering.
- The commander row gets `RightTag` = `⚑ n  ● m` (from `needsYouCount()` plus an in-progress count). Remove the `⚑ n need you` footer line from `View`.
- Accordion: `workstreamRows(rank, ws, expanded, tagProject)` gets a `showSteps bool`. `rebuildTree` passes true only for `focusWS()`, which is the cursor's workstream when focused, else `c.ownWS`. Collapsed rows set `RightTag` = `⚑ n` (Attn, if >0) + `● n` / `○ n`. Insert the expanded meta line (`ProjectChip(name) · k of n done`) as a `Skip` node.
- `tagProject` and the right-aligned project name go away (grouped mode keeps project headers).
- Re-run `rebuildTree` when the cursor moves between workstreams, because the expansion follows the cursor. The `followID` logic already keeps the cursor stable.
- Tests: `column_objectives_*_test.go` updates; add `TestAccordionFollowsCursor`.

**S5. FLEET block at the bottom-left** (`widgets/metrics.go`, `column_objectives.go`)
- Add `(*Metrics).Compact(width int) string`: 2 lines, or 3 with the sparkline at width ≥ 44. For the sparkline, add `CostSeries24h []float64` to `FleetMetricsLive` in `commander_data.go`, bucketing `cost_events` by hour.
- `ObjectivesColumn` already loads `CommanderLiveData`, so feed `d.Metrics` into it. Append `SectionLabel("FLEET")` + `Compact` to the `tail` above the hints.
- `f` opens a built-in `fleet` tab in the inspector (after S8).

**S6. Title bar** (`column_chat_title.go`)
- `accentFor("commander")` → Mauve. Keep Sapphire / Teal.
- Row 1: pill (`Background(kind).Foreground(Crust).Bold.Padding(0,1)`) + title + `╱` fill + meta. Row 2: status line on BgChrome. Width rules from §2.3 (`compact := c.width < 100`).
- Data: add `@cmdr_meta` (cwd · model) next to `@cmdr_title` in `layout.setWindowTitle`. Load `CommanderLiveData` on tick for row 2. Add `WithReportFocus` only if a focus tint is wanted (optional).
- Then §4's gradient wordmark, spinner and context meter as follow-up PRs.

**S7. Hints on focus only** (all `column_*.go`)
- Every column renders `HintLine` only while `focused`. Add `tea.WithReportFocus()` to the `tea.NewProgram` calls in `column_fleet.go`, `column_lead_fleet.go`, `column_operator_side.go` and the new inspector.

**S8. Inspector skeleton**
- New `internal/store/migrations/0005_inspector_tabs.sql` (§2.4) and store methods `ListTabs`, `AddTab`, `RemoveTab`, `MoveTab`, `SetActiveTab`, `SaveTabCache`.
- `cmd/cmdr/main.go`: add a `tab` subcommand with `add|rm|ls|focus|move`, defaulting the target from `$TMUX_PANE` → `#{@cmdr_ws}` + the window's project. Add `case "inspector":` to `__column`.
- New `internal/tui/column_inspector.go`: tab strip (§2.4), `bubbles/viewport` body, link-card renderer only, and keys `1-9 [ ] o y r x a J/K`. `a` opens an inline URL prompt; reuse `startPrompt` / `updatePrompt` from `column_objectives.go`.
- `internal/layout`: the right column for every window becomes `buildColumnCmdWithFlags("inspector", "--ws", bead, "--project", p)` in place of `cmdr-fleet` / `lead-fleet`. Window 0 uses no flags.
- Keep `operator-side` for operator windows. It can later become the operator's default tabs (worktree, container, ceilings) as a built-in renderer.
- Eval scenario: `tests/evals/scenarios/inspector_tab_add.json` (Manager runs `cmdr tab add` → tab visible).
- `lead.md` prompt paragraph (§2.4).

**S9. v1 renderers** (new `internal/inspector`, kept UI-agnostic per house style)
- `Detect(target) Renderer` → `github-pr` (`gh pr view N -R o/r --json title,state,author,headRefName,additions,deletions,changedFiles,statusCheckRollup,reviewDecision,reviews,body,updatedAt`), `linear` (GraphQL with `LINEAR_API_KEY`), `notion` (API → markdown), `web` (readability → markdown), `cmd` (exec with timeout, ANSI preserved), and `link`.
- Each returns a `Card{Title, State, URL, Meta []string, Sections []Section, Markdown string}`. The TUI renders it; body markdown goes through `glamour` with a custom style JSON built from the theme roles. Don't use glamour's auto style: its dark theme brings its own colors.
- Fetches run off the UI goroutine. Results are cached in `inspector_state.cache_json` and re-fetched every `refresh_secs`, on `r`, or when the tab is activated if stale.

**S10. Absolute widths and resize**
- `layoutThreeColumns`: `split-window -l <cols>` from the §2.9 formula instead of `-p`.
- Add `cmdr __relayout` (resize-pane `-x` per window), registered with `set-hook -t commander client-resized 'run-shell "cmdr __relayout"'` in `ensureCommanderSessionChrome`.
- Below 140 cols, the inspector is zoomed away: `resize-pane -x 1`, or `break-pane` into `_tabs` and back on `C-a i`.

**S11. Polish pass (§4)**
- Spinner and ⚑ pulse.
- Loading skeleton plus shimmer (shared `widgets.Skeleton(rows, width, frame)`).
- Error cards.
- Gradient wordmark.
- Context meter.

**S12. v2 inspector**
- `term` tabs via swap-pane plus the border tab strip.
- `image` tabs via kitty Unicode placeholders, with a half-block fallback (§4).

**Order rationale.**
- S1–S3 fix everything in the user's screenshot that isn't structural, in three small PRs with no new data.
- S4–S7 deliver the agreed layout.
- S8–S9 deliver the inspector.
- S10–S12 are polish and ambition, so they can never block the basics.
