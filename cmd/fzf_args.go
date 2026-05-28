package cmd

import "strings"

// sessionTreeFzfArgs returns the common session-tree picker arguments,
// including the key that re-closes the popup while fzf has focus.
func sessionTreeFzfArgs() []string {
	return append([]string{
		"--ansi",
		"--ignore-case",
		"--with-nth", "2",
		"--nth", "2",
		"--tiebreak", "begin,index",
	}, fzfAbortBinds("alt-s")...)
}

// windowFzfArgs returns the window picker arguments. It accepts both Alt+u
// (the user's local binding) and Alt+w (tmx init's default) as close keys.
func windowFzfArgs() []string {
	return append([]string{
		"--ignore-case",
		"--with-nth", "2..",
		"--nth", "2..",
		"--tiebreak", "begin,index",
		"--preview", "tmux capture-pane -ep -t {1}",
		"--preview-window", "right:50%",
	}, fzfAbortBinds("alt-u", "alt-w")...)
}

// paneFzfArgs returns the pane picker arguments, including Alt+p as a close key
// so the key that opened the popup can also dismiss it while fzf has focus.
func paneFzfArgs() []string {
	return append([]string{
		"--ignore-case",
		"--with-nth", "2..",
		"--nth", "2..",
		"--preview", "tmux capture-pane -ep -t {1}",
		"--preview-window", "right:50%",
	}, fzfAbortBinds("alt-p")...)
}

// fzfAbortBinds maps opener keys to fzf's abort action. This is what makes
// display-popup feel like a toggle even though tmux itself only opened it.
func fzfAbortBinds(keys ...string) []string {
	if len(keys) == 0 {
		return nil
	}
	binds := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		binds = append(binds, key+":abort")
	}
	if len(binds) == 0 {
		return nil
	}
	return []string{"--bind", strings.Join(binds, ",")}
}
