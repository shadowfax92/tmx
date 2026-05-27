package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"tmx/internal/tmux"
)

type paneLabelInputs struct {
	cwd      string
	home     string
	branch   string
	repoRoot string
	headSha  string
}

// autoPaneLabel infers a label for the calling pane from its cwd. Order:
//  1. git feature branch        → branch name
//  2. git repo on main/master   → repo folder name
//  3. detached HEAD             → <repo>@<short-sha>
//  4. $HOME                     → "home"
//  5. any other dir             → folder basename
//
// This is the only place tmx shells out to git, and only to read labels.
func autoPaneLabel() (string, error) {
	paneID, err := tmux.PaneID()
	if err != nil {
		return "", fmt.Errorf("reading pane id: %w", err)
	}
	cwd, err := tmux.PaneCwd(paneID)
	if err != nil {
		return "", fmt.Errorf("reading pane cwd: %w", err)
	}

	in := paneLabelInputs{cwd: cwd}
	in.home, _ = os.UserHomeDir()
	in.branch = gitCurrentBranch(cwd)
	in.repoRoot = gitRepoRoot(cwd)
	if in.branch == "" && in.repoRoot != "" {
		in.headSha = gitHeadShortSha(cwd)
	}

	return resolvePaneLabel(in), nil
}

func resolvePaneLabel(in paneLabelInputs) string {
	if in.branch != "" {
		if isMainBranch(in.branch) && in.repoRoot != "" {
			return filepath.Base(in.repoRoot)
		}
		return in.branch
	}
	if in.repoRoot != "" && in.headSha != "" {
		return filepath.Base(in.repoRoot) + "@" + in.headSha
	}
	if in.home != "" && samePath(in.cwd, in.home) {
		return "home"
	}
	if base := filepath.Base(in.cwd); base != "" && base != "." && base != string(filepath.Separator) {
		return base
	}
	return ""
}

func isMainBranch(branch string) bool {
	switch branch {
	case "main", "master", "trunk":
		return true
	}
	return false
}

func samePath(a, b string) bool {
	pa, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	pb, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	return pa == pb
}

func gitCurrentBranch(dir string) string {
	return gitOutput(dir, "symbolic-ref", "--short", "HEAD")
}

func gitRepoRoot(dir string) string {
	return gitOutput(dir, "rev-parse", "--show-toplevel")
}

func gitHeadShortSha(dir string) string {
	return gitOutput(dir, "rev-parse", "--short", "HEAD")
}

func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
