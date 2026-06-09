<div align="center">

# 🧭 tmx

**Get around tmux — and keep a scratch popup one keystroke away.**

*A colored session tree, window/pane jump with live previews, pane labels, and recreatable scratch popups. No worktrees, no daemon, no state file.*

</div>

`tmx` is the navigation half of [grove](https://github.com/shadowfax92/grove), carved out into its own tool. grove owns git worktrees; `tmx` owns *getting around tmux*: a session tree you land on, fuzzy jump to any window or pane, moving and labeling what's in front of you, and **scratch popups** — recreatable vim/shell/lazygit overlays bound to the current pane. It talks only to `tmux` (and to `git` once, to guess a pane label). It never reads grove's config or state.

- 🌳 **Session tree as the landing view** — `tmx` opens a colored tree of your sessions grouped by `/`, each annotated with the command it's running. Pick one, switch to it.
- 🔭 **Jump to any window or pane** — `tmx -w` / `tmx -p` fuzzy-search every window/pane with a live `capture-pane` preview on the right.
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
```

If you also use rmux inside tmux, install rmux scratch bindings separately from
inside a running rmux server:

```sh
tmx init --rmux
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

tmx reap            # kill orphaned / idle / dead-cwd scratch sessions
tmx reap --dry-run  # preview what would be reaped
tmx reap --ttl 1h   # override the idle threshold
tmx reap --all      # kill every scratch session

tmx init         # (re)install the tmux keybindings
tmx init --rmux  # install rmux scratch keybindings
tmx config       # show the active width profile + resolved popup sizes
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

Pass `tmx init --no-jump` to skip the `M-s/M-w/M-p` binds.

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

  # Optional keys 'tmx init --rmux' binds inside rmux. Missing entries fall
  # back to keys; empty entries skip that scratch type.
  rmux_keys:
    vim: "M-I"
    sh: "M-O"

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
      popups:
        vim: { width: "95%", height: "95%" }
        sh:  { width: "95%", height: "95%" }
```

Profiles are matched against the tmux client width (`#{client_width}`), first
match wins; force one with `TMX_PROFILE=<name>`. Run `tmx config` to see the
current width, the active profile, and the size each type resolves to.

## How scratch popups work

A scratch session is a throwaway tmux session named `gs/<type>/<pane-id>`, bound
to the pane that opened it. The `gs/` prefix exists for one reason: navigation
hides it from the default views (surface it with `-a`).

When `tmx scratch` runs from an rmux-spawned process, it targets rmux instead of
the outer tmux server. In nested rmux-inside-tmux setups, unchanged outer tmux
`bind-key -n` bindings still belong to tmux and will not reach rmux. Use
`tmx init --rmux` plus rmux-visible keys, usually via `scratch.rmux_keys`, for
popups that open inside the rmux UI.

Because scratch sessions are **recreatable** — rebuilt from the parent pane's
cwd on the next toggle — aggressive reaping is safe. `tmx reap` kills a scratch
when any of these holds:

- **orphan** — the parent pane is gone
- **dead-cwd** — its start directory no longer exists on disk
- **idle** — untouched longer than `scratch.ttl`

Reaping is a manual sweep — run `tmx reap` (or `tmx reap --dry-run` to preview)
when the `gs/` namespace gets cluttered. It deliberately does **not** run on
toggle: that put a full namespace scan on the popup-open hot path, which got
slow with a large backlog. To automate it, wrap `tmx reap` in a cron/`loop`.

> Scratch sessions keep grove's `gs/` prefix and `shadow_*` session vars, so a
> previous grove install's popups are adopted automatically. The shell env
> exported into a popup is tmx-native (`TMX_SCRATCH`, `TMX_SCRATCH_TYPE`,
> `TMX_PARENT_PANE`).
