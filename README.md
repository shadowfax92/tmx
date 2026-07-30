<div align="center">

# 🧭 tmx

**Get around tmux — and know which agent needs you next.**

*A colored session tree, window/pane jump with live previews, agent attention queue, pane labels, and recreatable scratch popups. Live attention state stays in tmux.*

</div>

`tmx` is the tmux companion to [grove](https://github.com/shadowfax92/grove), carved out into its own tool. grove owns git worktrees; `tmx` owns *getting around tmux and keeping track of agent panes*: a session tree you land on, fuzzy jump to any window or pane, attention state, moving and labeling what's in front of you, and **scratch popups** — recreatable vim/shell/lazygit overlays bound to the current pane. It never reads grove's config or state.

- 🌳 **Session tree as the landing view** — `tmx` opens a colored tree of your sessions grouped by `/`, each annotated with the command it's running. Pick one, switch to it.
- 🔭 **Jump to any window or pane** — `tmx -w` / `tmx -p` fuzzy-search every window/pane with a live `capture-pane` preview on the right.
- 🔵 **Agent attention queue** — a tmux-scoped watcher flags quiet agent panes, colors unread windows, and jumps to the oldest unread pane with `M-u u`.
- 🪄 **Scratch popups** — one keybind toggles a popup session (nvim, shell, lazygit, …) rooted at the current pane's directory. Config-driven: any command, any size.
- 🧹 **One cleanup command** — `tmx reap` kills scratch sessions that are orphaned, idle past a TTL, or rooted in a directory that no longer exists. One `list-sessions` + one `list-panes`, so it's instant even with a big `gs/` backlog.
- 🏷️ **Pane labels** — `tmx rename` labels the current pane from its git branch / repo / folder; `-w` renames the window too.
- 🔀 **Move windows between sessions** — `tmx move` picks one or more source windows and a destination session, creating the target session if needed.
- 🪶 **Self-contained** — a single Go binary, fzf for the picker, no shared library with grove.

---

## Install

Requires Go 1.24+, tmux 3.3+, and [fzf](https://github.com/junegunn/fzf).

```sh
make install        # builds and copies to ~/bin/tmx (codesigned)
```

Then install the keybindings in your running tmux server (and add the one-liner
to `~/.tmux.conf` so they survive a server restart):

```sh
tmx init
```

```tmux
# ~/.tmux.conf
run-shell 'tmx init'
run-shell 'tmx watch start'
```

## Usage

```sh
tmx              # colored session tree → switch to the selection
tmx -w           # jump to a window (with live pane preview)
tmx -p           # jump to a pane    (with live pane preview)
tmx -a           # include scratch (gs/) sessions in any of the above

tmx move         # pick windows, then pick the session to move them into
tmx move admin   # pick windows, then move them to "admin" (created if missing)
tmx rename       # label the current pane from git/cwd
tmx rename -w    # …and rename the window, disabling automatic-rename
tmx rename --clear

tmx scratch vim  # toggle the vim scratch popup for this pane
tmx scratch sh   # toggle a shell popup
tmx promote dev  # turn the current scratch session into a real session "dev"

tmx jump            # jump to the oldest unread agent pane
tmx inbox           # pick a watched pane with fzf (plain listing when not on a TTY)
tmx inbox --plain   # list watched panes without fzf
tmx mark unread     # flag the current agent pane
tmx mark read       # clear its unread flag
tmx snooze          # move the current pane to the back of the unread queue
tmx unwatch         # opt the current pane out until its agent process changes

tmx watch start       # start this tmux server's detached attention watcher
tmx watch status
tmx watch stop        # pick watched panes to unwatch (Tab selects several)
tmx watch stop -t %3 work:2.0  # unwatch explicit targets without fzf
tmx watch stop --daemon        # stop the detached watcher daemon
tmx watch reap        # interactively remove stale watched panes
tmx watch reap --dry-run

tmx reap            # kill orphaned / idle / dead-cwd scratch sessions
tmx reap --dry-run  # preview what would be reaped
tmx reap --ttl 1h   # override the idle threshold
tmx reap --all      # kill every scratch session

tmx init            # (re)install the tmux keybindings
tmx init --attn-conf  # print attention status-format fragments
tmx config          # show the active width profile + resolved popup sizes
tmx config --edit  # open the config in $EDITOR
```

Default keybinds (from `tmx init`):

| Key   | Action                      |
| ----- | --------------------------- |
| `M-v` | toggle the `vim` scratch    |
| `M-b` | toggle the `sh` scratch     |
| `M-s` | session tree popup (`tmx`)  |
| `M-w` | window jump popup (`tmx -w`)|
| `M-p` | pane jump popup (`tmx -p`)  |
| `M-u` | enter the `attn` key-table  |

Inside the attention key-table:

| Key | Action |
| --- | ------ |
| `u` | jump to the oldest unread pane; repeat `u` to walk the queue |
| `s` | snooze the current pane |
| `i` | open the attention inbox |
| `d` | stop watching the current pane |

Any unbound key cancels the table. `M-y` is intentionally left untouched.
Pass `tmx init --no-jump` to skip only the old `M-s/M-w/M-p` popup binds;
scratch and attention binds are still installed.

> Before reloading your config, delete the old manual `M-u` binding at
> `~/.tmux.conf:141` (the one that duplicated the `M-w` window jump). A later
> manual bind shadows the attention table installed by `tmx init`.

## Config

Location: `~/.config/tmx/config.yaml` (created on first run).

```yaml
scratch:
  # Kill scratch sessions idle longer than this (s, m, h, or d for days).
  ttl: 6h

  # Keys 'tmx init' binds. Each maps a scratch type to a tmux key.
  keys:
    vim: "M-v"
    sh: "M-b"

  # Per-type popups: the command to run (empty = login shell) and the size.
  popups:
    vim: { cmd: nvim, width: "80%", height: "95%" }
    sh:  { cmd: "",   width: "90%", height: "95%" }
    git: { cmd: lazygit, width: "90%", height: "90%" }

  # Optional: override popup sizes for clients matching a width band, or
  # selected explicitly via $TMX_PROFILE.
  profiles:
    - name: laptop
      match: { max_client_width: 310 }
      jump_action: zoom
      popups:
        vim: { width: "95%", height: "95%" }
        sh:  { width: "95%", height: "95%" }

watch:
  poll: 3s
  grace_periods: 3
  period: 30s
  capture_lines: 30
  agents: [claude, codex]
  reap_ttl: 24h
  # Show an empty `tmx jump` via tmux (default) or mac-notify.
  inbox_zero: mac-notify
  # Used when the selected profile has jump_action: focus.
  # focus_command: '~/.tmux/focus-inplace.sh focus "{pane}"'
```

Profiles are matched against the tmux client width (`#{client_width}`), first
match wins; force one with `TMX_PROFILE=<name>`. Run `tmx config` to see the
current width, the active profile, and the size each type resolves to.

## Agent attention

`tmx watch start` launches one detached watcher for the current tmux server.
It discovers panes whose process tree contains a configured agent, excluding
scratch (`gs/`) sessions and `@fip_buffer` panes. Every `poll`, it hashes the
last `capture_lines` lines. A stable pane becomes unread after
`grace_periods × period` (90 seconds by default), once per quiet episode.
Live pane/window state stays in tmux; watcher lifecycle files (`watch.pid`,
`watch.lock`, and `watch.log`) live in a per-socket directory below
`~/.local/state/tmx/` by default. `TMX_STATE_DIR` or `XDG_STATE_HOME` can
override that location.

Visiting an unread pane clears it within one watcher tick; `tmx jump` clears it
immediately. Set `watch.inbox_zero: mac-notify` to send an empty-queue result
through `mac-notify`; if the helper is unavailable, tmx falls back to its tmux
status message. For instant focus-driven clearing regardless of the watcher
poll interval, add this hook to your tmux configuration:

```tmux
set-hook -g pane-focus-in "run-shell 'tmx mark read --if-unread -t \"#{hook_pane}\"'"
```

The `--if-unread` mode is a silent no-op unless the target pane is unread. The
hook passes `#{hook_pane}` explicitly because it identifies the pane that fired
the event. `tmx init` does not install this hook.

New screen activity re-arms the episode. `tmx snooze` gives the current pane a
fresh unread timestamp so it moves to the back of the oldest-first queue.
`tmx unwatch` is an explicit opt-out; it re-arms when the foreground agent
process changes. `tmx watch stop` applies the same opt-out to multiple panes,
using an fzf multi-select when no targets are supplied. `tmx watch stop
--daemon` stops the detached watcher process. `tmx watch reap` handles
abandoned panes older than `watch.reap_ttl`, with a dry-run and y/N
confirmation.

Run the watcher on every server start alongside the key initialization:

```tmux
run-shell 'tmx init'
run-shell 'tmx watch start'
```

### Status visuals

`tmx init` installs keys only; it never edits status formats. Print the two
paste-ready fragments with:

```sh
tmx init --attn-conf
```

Use the first fragment in both `window-status-format` and
`window-status-current-format` where your theme renders the window index/name.
It renders windows containing unread panes in blue:

```tmux
#{?#{e|>:#{@attn_unread_count},0},#[fg=blue]#{window_index}:#{window_name}#[default],#{window_index}:#{window_name}}
```

Append the second fragment inside each active/inactive branch of your existing
`pane-border-format`, next to badges such as `@pane_label` and `[FOCUS]`:

```tmux
#{?#{==:#{@attn_state},unread},#[fg=blue] [ATTN]#[default],}
```

Merge these fragments into the formats your theme already owns instead of
replacing the complete format strings.

### Tmux state contract

Attention state lives in tmux user options, so it disappears with the panes
and server it describes. The watcher and attention commands maintain:

| Option | Scope | Meaning |
| ------ | ----- | ------- |
| `@attn_watch` | pane | `1` when watched, `0` for an explicit opt-out |
| `@attn_state` | pane | `active`, `quiet`, or `unread` |
| `@attn_since` | pane | Unix time when the current state began |
| `@attn_changed` | pane | Unix time of the last material screen change |
| `@attn_proc` | pane | process fingerprint used to detect a new task |
| `@attn_read_hash` | pane | hash of the screen tail acknowledged by the most recent read |
| `@attn_read_lines` | pane | line count used to calculate the acknowledged screen hash |
| `@attn_unread_count` | window | number of unread panes in that window |

Status formats should read only `@attn_state` and `@attn_unread_count`; use
`tmx mark`, `tmx snooze`, and `tmx unwatch` rather than editing options by hand.
The watcher keeps its rolling screen hash and per-episode fired flag private.

## How scratch popups work

A scratch session is a throwaway tmux session named `gs/<type>/<pane-id>`, bound
to the pane that opened it. The `gs/` prefix exists for one reason: navigation
hides it from the default views (surface it with `-a`).

Because scratch sessions are **recreatable** — rebuilt from the parent pane's
cwd on the next toggle — aggressive reaping is safe. `tmx reap` kills a scratch
when any of these holds:

- **orphan** — the parent pane is gone
- **dead-cwd** — its start directory no longer exists on disk
- **idle** — untouched longer than `scratch.ttl`

Reaping is a manual sweep — run `tmx reap` (or `tmx reap --dry-run` to preview)
when the `gs/` namespace gets cluttered. Normal toggles deliberately do **not**
run a full namespace scan: that put the popup-open hot path on the size of the
`gs/` backlog. A toggle from a stale scratch whose stored parent pane is already
gone may run the same orphan/dead-cwd cleanup before returning. To automate
regular idle cleanup, wrap `tmx reap` in a cron/`loop`.

> Scratch sessions keep grove's `gs/` prefix and `shadow_*` session vars, so a
> previous grove install's popups are adopted automatically. The shell env
> exported into a popup is tmx-native (`TMX_SCRATCH`, `TMX_SCRATCH_TYPE`,
> `TMX_PARENT_PANE`).
