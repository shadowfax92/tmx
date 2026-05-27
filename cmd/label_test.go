package cmd

import "testing"

func TestResolvePaneLabelPrefersFeatureBranch(t *testing.T) {
	got := resolvePaneLabel(paneLabelInputs{
		cwd:      "/code/mono/.worktrees/feat-auth",
		repoRoot: "/code/mono/.worktrees/feat-auth",
		branch:   "feat-auth",
	})
	if got != "feat-auth" {
		t.Fatalf("resolvePaneLabel() = %q, want feat-auth", got)
	}
}

func TestResolvePaneLabelMainBranchUsesRepoFolder(t *testing.T) {
	got := resolvePaneLabel(paneLabelInputs{
		cwd:      "/code/mono",
		repoRoot: "/code/mono",
		branch:   "main",
	})
	if got != "mono" {
		t.Fatalf("resolvePaneLabel() = %q, want mono", got)
	}
}

func TestResolvePaneLabelDetachedHeadUsesRepoAtSha(t *testing.T) {
	got := resolvePaneLabel(paneLabelInputs{
		cwd:      "/code/mono",
		repoRoot: "/code/mono",
		headSha:  "abc1234",
	})
	if got != "mono@abc1234" {
		t.Fatalf("resolvePaneLabel() = %q, want mono@abc1234", got)
	}
}

func TestResolvePaneLabelHomeIsNamedHome(t *testing.T) {
	got := resolvePaneLabel(paneLabelInputs{
		cwd:  "/Users/x",
		home: "/Users/x",
	})
	if got != "home" {
		t.Fatalf("resolvePaneLabel() = %q, want home", got)
	}
}

func TestResolvePaneLabelFallsBackToFolderBasename(t *testing.T) {
	got := resolvePaneLabel(paneLabelInputs{
		cwd:  "/some/random/notes",
		home: "/Users/x",
	})
	if got != "notes" {
		t.Fatalf("resolvePaneLabel() = %q, want notes", got)
	}
}
