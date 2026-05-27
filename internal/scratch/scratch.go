// Package scratch manages tmx's recreatable popup sessions.
//
// Scratch sessions are throwaway tmux sessions bound to a parent pane, surfaced
// as popups (vim, shell, lazygit, …). They carry the reserved "gs/" prefix so
// navigation can hide them from the default views, and they are recreatable
// (rebuilt from the parent pane's cwd on the next toggle), which makes
// aggressive reaping safe.
//
// On-the-wire compatibility: the prefix and the tmux session-var keys are kept
// as "gs/" and "shadow_*" so tmx adopts any scratch sessions a previous grove
// install left behind. The env exported into the popup shell is tmx-native
// (TMX_*), and EnvVersion is bumped so an inherited session is recreated once
// with the new env on its first toggle.
package scratch

import (
	"fmt"
	"os"
	"strings"
	"time"

	"tmx/internal/tmux"
)

const Prefix = "gs"

// EnvVersion forces recreation of sessions built under an older env scheme.
// Bumped past grove's "1" so inherited gs/ sessions refresh once with TMX_ env.
const EnvVersion = "2"

const (
	cwdKey           = "shadow_cwd"
	parentPaneKey    = "shadow_parent_pane"
	envVersionKey    = "shadow_env_version"
	openedAtKey      = "shadow_opened_at"
	lastToggledAtKey = "shadow_last_toggled_at"
)

// Indirections for testing seams.
var (
	listSessionSnapshotsByPrefix = tmux.ListSessionSnapshotsByPrefix
	getSessionVar                = tmux.GetSessionVar
	setSessionVar                = tmux.SetSessionVar
	sessionExists                = tmux.SessionExists
	newSessionWithCommand        = tmux.NewSessionWithCommand
	paneExists                   = tmux.PaneExists
	killSession                  = defaultKillSession
	pathExists                   = defaultPathExists
	now                          = time.Now
)

type ReapReason string

const (
	ReapOrphan  ReapReason = "orphan"
	ReapDeadCwd ReapReason = "dead-cwd"
	ReapIdle    ReapReason = "idle"
	ReapAll     ReapReason = "all"
)

type ReapOptions struct {
	TTL    time.Duration // idle threshold; 0 disables idle reaping
	All    bool          // reap every scratch session regardless of reason
	DryRun bool          // select only, don't kill
}

type ReapCandidate struct {
	SessionName   string
	Type          string
	ParentPane    string
	Cwd           string
	OpenedAt      time.Time
	LastToggledAt time.Time
	LastActiveAt  time.Time
	Reason        ReapReason
}

type ReapFailure struct {
	Candidate ReapCandidate
	Err       error
}

type ReapReport struct {
	Matched []ReapCandidate
	Removed []ReapCandidate
	Failed  []ReapFailure
}

type scratchSessionState struct {
	name          string
	typ           string
	parentPane    string
	cwd           string
	openedAt      time.Time
	lastToggledAt time.Time
	lastActiveAt  time.Time
	orphan        bool
}

func Name(paneID, typ string) string {
	id := strings.TrimPrefix(paneID, "%")
	return fmt.Sprintf("%s/%s/%s", Prefix, typ, id)
}

func IsSession(name string) bool {
	return strings.HasPrefix(name, Prefix+"/")
}

// ParentPane resolves the pane a scratch session is bound to. When called from
// outside a scratch session it returns the active pane; from inside one it
// reads the stored parent so the popup toggles against the right pane.
func ParentPane(currentSession, activePane string) (string, error) {
	if !IsSession(currentSession) {
		return activePane, nil
	}
	paneID, err := getSessionVar(currentSession, parentPaneKey)
	if err != nil {
		return "", fmt.Errorf("getting scratch parent pane: %w", err)
	}
	if paneID == "" {
		return "", fmt.Errorf("scratch session %s is missing %s", currentSession, parentPaneKey)
	}
	return paneID, nil
}

// PopupClient resolves which tmux client owns the popup. From inside a scratch
// session it reads the stored client so closing toggles the right popup.
func PopupClient(currentSession, fallback string) (string, error) {
	if !IsSession(currentSession) {
		return fallback, nil
	}
	clientName, err := getSessionVar(currentSession, "shadow_client_name")
	if err != nil || clientName == "" {
		return fallback, nil
	}
	return clientName, nil
}

// Ensure creates or re-creates a scratch session for the given pane. If the
// session exists but its cwd no longer matches paneCwd (or it was built under
// an older env scheme), it is killed and recreated so the scratch always
// follows the pane's project. command is the popup's program (empty = shell).
func Ensure(sessionName, paneCwd, typ, paneID, command string) error {
	if sessionExists(sessionName) {
		storedCwd, _ := getSessionVar(sessionName, cwdKey)
		envVersion, _ := getSessionVar(sessionName, envVersionKey)
		if storedCwd == paneCwd && envVersion == EnvVersion {
			return nil
		}
		killSession(sessionName)
	}

	env := []string{
		fmt.Sprintf("TMX_PARENT_PANE=%s", paneID),
		"TMX_SCRATCH=1",
		fmt.Sprintf("TMX_SCRATCH_TYPE=%s", typ),
	}

	if err := newSessionWithCommand(sessionName, paneCwd, env, command); err != nil {
		return fmt.Errorf("creating scratch session: %w", err)
	}
	openedAt := now().UTC().Format(time.RFC3339)
	if err := setSessionVar(sessionName, cwdKey, paneCwd); err != nil {
		return fmt.Errorf("storing scratch cwd: %w", err)
	}
	if err := setSessionVar(sessionName, parentPaneKey, paneID); err != nil {
		return fmt.Errorf("storing scratch parent pane: %w", err)
	}
	if err := setSessionVar(sessionName, envVersionKey, EnvVersion); err != nil {
		return fmt.Errorf("storing scratch env version: %w", err)
	}
	if err := setSessionVar(sessionName, openedAtKey, openedAt); err != nil {
		return fmt.Errorf("storing scratch opened timestamp: %w", err)
	}
	return nil
}

// MarkToggled records the latest explicit user interaction so a freshly toggled
// scratch isn't reaped for idleness right after the user touched it.
func MarkToggled(sessionName string) error {
	return setSessionVar(sessionName, lastToggledAtKey, now().UTC().Format(time.RFC3339))
}

// SelectReapCandidates returns the scratch sessions that should be killed under
// the given options. A session is a candidate when any of these holds (checked
// in order): --all, orphaned parent pane, dead start directory, or idle past
// the TTL.
func SelectReapCandidates(opts ReapOptions) ([]ReapCandidate, error) {
	sessions, err := listScratchSessions()
	if err != nil {
		return nil, err
	}

	current := now()
	candidates := make([]ReapCandidate, 0, len(sessions))
	for _, session := range sessions {
		if reason, ok := reapReason(session, opts, current); ok {
			candidates = append(candidates, session.candidate(reason))
		}
	}
	return candidates, nil
}

func reapReason(s scratchSessionState, opts ReapOptions, current time.Time) (ReapReason, bool) {
	switch {
	case opts.All:
		return ReapAll, true
	case s.orphan:
		return ReapOrphan, true
	case s.cwd != "" && !pathExists(s.cwd):
		return ReapDeadCwd, true
	case opts.TTL > 0:
		last := s.lastActiveAt
		if s.lastToggledAt.After(last) {
			last = s.lastToggledAt
		}
		if !last.IsZero() && current.Sub(last) >= opts.TTL {
			return ReapIdle, true
		}
	}
	return "", false
}

// Reap selects and kills scratch sessions. With DryRun it only reports matches.
func Reap(opts ReapOptions) (ReapReport, error) {
	matched, err := SelectReapCandidates(opts)
	if err != nil {
		return ReapReport{}, err
	}

	report := ReapReport{Matched: matched}
	if opts.DryRun {
		return report, nil
	}

	for _, candidate := range matched {
		if err := killSession(candidate.SessionName); err != nil {
			report.Failed = append(report.Failed, ReapFailure{Candidate: candidate, Err: err})
			continue
		}
		report.Removed = append(report.Removed, candidate)
	}

	if len(report.Failed) > 0 {
		return report, fmt.Errorf("failed to remove %d scratch sessions", len(report.Failed))
	}
	return report, nil
}

// ReapOnToggle runs a best-effort sweep (orphan + dead-cwd + idle) so the
// scratch namespace self-cleans on normal use, with no reliance on a tmux hook.
func ReapOnToggle(ttl time.Duration) {
	_, _ = Reap(ReapOptions{TTL: ttl})
}

func listScratchSessions() ([]scratchSessionState, error) {
	snapshots, err := listSessionSnapshotsByPrefix(Prefix + "/")
	if err != nil {
		return nil, err
	}

	sessions := make([]scratchSessionState, 0, len(snapshots))
	for _, snapshot := range snapshots {
		session := scratchSessionState{
			name:          snapshot.Name,
			typ:           sessionType(snapshot.Name),
			openedAt:      sessionMetadataTime(snapshot.Name, openedAtKey, snapshot.Created),
			lastToggledAt: sessionMetadataTime(snapshot.Name, lastToggledAtKey, snapshot.Activity),
			lastActiveAt:  snapshot.Activity,
		}
		session.cwd, _ = getSessionVar(snapshot.Name, cwdKey)

		parentPane, err := getSessionVar(snapshot.Name, parentPaneKey)
		if err != nil || parentPane == "" {
			session.orphan = true
		} else {
			session.parentPane = parentPane
			session.orphan = !paneExists(parentPane)
		}

		sessions = append(sessions, session)
	}
	return sessions, nil
}

func sessionType(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) != 3 {
		return ""
	}
	return parts[1]
}

func sessionMetadataTime(sessionName, key string, fallback time.Time) time.Time {
	raw, err := getSessionVar(sessionName, key)
	if err != nil || strings.TrimSpace(raw) == "" {
		return fallback
	}
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return ts.UTC()
}

func (s scratchSessionState) candidate(reason ReapReason) ReapCandidate {
	return ReapCandidate{
		SessionName:   s.name,
		Type:          s.typ,
		ParentPane:    s.parentPane,
		Cwd:           s.cwd,
		OpenedAt:      s.openedAt,
		LastToggledAt: s.lastToggledAt,
		LastActiveAt:  s.lastActiveAt,
		Reason:        reason,
	}
}

func defaultKillSession(name string) error {
	return tmux.KillSession(name)
}

// defaultPathExists reports whether path is present. A definite "not found"
// returns false (reapable); permission and other ambiguous errors return true
// so a transient failure never kills a session out from under the user.
func defaultPathExists(path string) bool {
	if path == "" {
		return true
	}
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}
