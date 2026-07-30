// Package attn discovers and tracks panes that may need user attention.
package attn

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"tmx/internal/tmux"
)

var (
	listPaneInfo  = tmux.ListPaneInfo
	loadProcesses = readProcessSnapshot
)

type processInfo struct {
	command string
}

type processSnapshot struct {
	byPID    map[int]processInfo
	children map[int][]int
}

// Discover returns live agent panes from every session on the tmux server.
// Scratch sessions and focus-in-place buffer panes are excluded before the
// process tree is inspected.
func Discover(agents []string) ([]tmux.PaneInfo, error) {
	agentNames := normalizedAgentNames(agents)
	if len(agentNames) == 0 {
		return nil, nil
	}

	panes, err := listPaneInfo()
	if err != nil {
		return nil, err
	}

	candidates := make([]tmux.PaneInfo, 0, len(panes))
	for _, pane := range panes {
		if strings.HasPrefix(pane.Session, "gs/") || pane.FIPBuffer || pane.PID <= 0 {
			continue
		}
		candidates = append(candidates, pane)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	processes, err := loadProcesses()
	if err != nil {
		return nil, err
	}

	discovered := make([]tmux.PaneInfo, 0, len(candidates))
	for _, pane := range candidates {
		if processes.matchesTree(pane.PID, agentNames) {
			discovered = append(discovered, pane)
		}
	}
	return discovered, nil
}

func normalizedAgentNames(agents []string) map[string]struct{} {
	names := make(map[string]struct{}, len(agents))
	for _, agent := range agents {
		name := normalizedCommand(agent)
		if name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

func normalizedCommand(command string) string {
	name := filepath.Base(strings.TrimSpace(command))
	name = strings.TrimPrefix(name, "-")
	return strings.ToLower(name)
}

func (s processSnapshot) matchesTree(rootPID int, agents map[string]struct{}) bool {
	if _, exists := s.byPID[rootPID]; !exists {
		// The pane disappeared after list-panes. Normal tmux churn is not an
		// error and the stale inventory entry can simply be ignored.
		return false
	}

	pending := []int{rootPID}
	visited := make(map[int]struct{})
	for len(pending) > 0 {
		pid := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if _, seen := visited[pid]; seen {
			continue
		}
		visited[pid] = struct{}{}

		process := s.byPID[pid]
		if _, matches := agents[normalizedCommand(process.command)]; matches {
			return true
		}
		pending = append(pending, s.children[pid]...)
	}
	return false
}

func readProcessSnapshot() (processSnapshot, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,comm=").CombinedOutput()
	if err != nil {
		return processSnapshot{}, fmt.Errorf("listing process tree: %s (%w)", strings.TrimSpace(string(out)), err)
	}

	snapshot := processSnapshot{
		byPID:    make(map[int]processInfo),
		children: make(map[int][]int),
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if pidErr != nil || parentErr != nil {
			continue
		}
		snapshot.byPID[pid] = processInfo{
			command: strings.Join(fields[2:], " "),
		}
		snapshot.children[parent] = append(snapshot.children[parent], pid)
	}
	return snapshot, nil
}
