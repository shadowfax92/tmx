package sessionmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"tmx/internal/config"
)

type Command struct {
	Program     string
	Args        []string
	Interactive bool
}

type Runner interface {
	Run(program string, args ...string) (string, error)
	RunInteractive(program string, args ...string) error
}

type ExecRunner struct{}

type Client struct {
	backend string
	prefix  string
	runner  Runner
}

func New(cfg config.SessionsConfig, runner Runner) Client {
	backend := strings.ToLower(strings.TrimSpace(cfg.Backend))
	if backend == "" {
		backend = config.DefaultSessionBackend
	}
	prefix := strings.Trim(strings.TrimSpace(cfg.Prefix), "/")
	if prefix == "" {
		prefix = config.DefaultSessionPrefix
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	return Client{backend: backend, prefix: prefix, runner: runner}
}

func (c Client) Backend() string { return c.backend }

func (c Client) Prefix() string { return c.prefix }

func (c Client) PhysicalSessionName(name string) string {
	name = strings.TrimPrefix(strings.TrimSpace(name), "=")
	if name == "" || c.prefix == "" || strings.HasPrefix(name, c.prefix+"/") {
		return name
	}
	return c.prefix + "/" + name
}

func (c Client) IsPhysicalSessionName(name string) bool {
	name = strings.TrimPrefix(strings.TrimSpace(name), "=")
	return c.prefix != "" && strings.HasPrefix(name, c.prefix+"/")
}

func (c Client) Plan(args []string) (Command, error) {
	if len(args) == 0 {
		return Command{}, fmt.Errorf("missing rmx command")
	}

	switch c.backend {
	case "tmux":
		return c.planTmux(args)
	case "rmux":
		return Command{Program: "rmux", Args: append([]string(nil), args...), Interactive: isInteractive(args[0])}, nil
	default:
		return Command{}, fmt.Errorf("unsupported sessions backend %q (want tmux or rmux)", c.backend)
	}
}

func (c Client) Run(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("missing rmx command")
	}
	if c.backend == "tmux" && canonicalCommand(args[0]) == "list-sessions" {
		return c.runTmuxListSessions(args)
	}

	cmd, err := c.Plan(args)
	if err != nil {
		return "", err
	}
	if cmd.Interactive {
		return "", c.runner.RunInteractive(cmd.Program, cmd.Args...)
	}
	return c.runner.Run(cmd.Program, cmd.Args...)
}

func (c Client) DryRunLine(args []string) (string, error) {
	if len(args) > 0 && args[0] == "exit" {
		return "+ " + shellJoin([]string{"tmx", "rmx", "exit"}) + "  # resolves current prefixed session at runtime", nil
	}
	cmd, err := c.Plan(args)
	if err != nil {
		return "", err
	}
	parts := append([]string{cmd.Program}, cmd.Args...)
	return "+ " + shellJoin(parts), nil
}

func (c Client) ExitCurrent() error {
	switch c.backend {
	case "tmux":
		name, err := c.runner.Run("tmux", "display-message", "-p", "#{session_name}")
		if err != nil {
			return err
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("could not determine current tmux session")
		}
		if !c.IsPhysicalSessionName(name) {
			return fmt.Errorf("tmx rmx exit must run from an %s/ tmux session (current: %s)", c.prefix, name)
		}
		_, err = c.runner.Run("tmux", "kill-session", "-t", "="+name)
		return err
	case "rmux":
		name, err := c.runner.Run("rmux", "display-message", "-p", "#{session_name}")
		if err != nil {
			return err
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("could not determine current rmux session")
		}
		_, err = c.runner.Run("rmux", "kill-session", "-t", "="+name)
		return err
	default:
		return fmt.Errorf("unsupported sessions backend %q (want tmux or rmux)", c.backend)
	}
}

func (c Client) planTmux(args []string) (Command, error) {
	command := canonicalCommand(args[0])
	mapped := append([]string{args[0]}, args[1:]...)

	switch command {
	case "new-session":
		if err := mapNewSessionFlagValues(mapped, map[string]func(string) string{
			"-s": c.PhysicalSessionName,
			"-t": func(value string) string { return c.physicalTarget(value, true, targetSession) },
		}); err != nil {
			return Command{}, err
		}
	default:
		if kind, ok := commandTargetKind(command); ok {
			if err := mapFirstTargetFlag(mapped, func(value string) string {
				return c.physicalTarget(value, true, kind)
			}); err != nil {
				return Command{}, err
			}
		}
	}

	return Command{Program: "tmux", Args: mapped, Interactive: isInteractive(command)}, nil
}

func (c Client) runTmuxListSessions(args []string) (string, error) {
	format, rest := listSessionsFormat(args[1:])
	runArgs := append([]string{"list-sessions"}, rest...)
	runArgs = append(runArgs, "-F", "#{session_name}\t"+format)

	out, err := c.runner.Run("tmux", runArgs...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", nil
	}

	var lines []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || !c.IsPhysicalSessionName(parts[0]) {
			continue
		}
		logical := strings.TrimPrefix(parts[0], c.prefix+"/")
		rendered := strings.ReplaceAll(parts[1], parts[0], logical)
		lines = append(lines, rendered)
	}
	return strings.Join(lines, "\n"), nil
}

func listSessionsFormat(args []string) (string, []string) {
	const defaultFormat = "#{session_name}: #{session_windows} windows (created #{session_created})"
	format := defaultFormat
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "-F" && i+1 < len(args) {
			format = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return format, rest
}

func mapFirstTargetFlag(args []string, mapper func(string) string) error {
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return nil
		}
		if hasCompactFlag(arg, 't') {
			return fmt.Errorf("compact -t targets are not supported; use -t <target>")
		}
		if arg != "-t" {
			continue
		}
		if i+1 >= len(args) {
			return fmt.Errorf("missing value for -t")
		}
		args[i+1] = mapper(args[i+1])
		return nil
	}
	return nil
}

func mapNewSessionFlagValues(args []string, mappers map[string]func(string) string) error {
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return nil
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return nil
		}
		if hasCompactFlag(arg, 's') {
			return fmt.Errorf("compact -s session names are not supported; use -s <session>")
		}
		if hasCompactFlag(arg, 't') {
			return fmt.Errorf("compact -t targets are not supported; use -t <target>")
		}

		if mapper, ok := mappers[arg]; ok {
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", arg)
			}
			args[i+1] = mapper(args[i+1])
			i++
			continue
		}
		if newSessionOptionConsumesValue(arg) {
			i++
		}
	}
	return nil
}

func newSessionOptionConsumesValue(arg string) bool {
	switch arg {
	case "-c", "-e", "-F", "-f", "-n", "-x", "-y":
		return true
	default:
		return false
	}
}

type targetKind int

const (
	targetSession targetKind = iota
	targetPane
)

func (c Client) physicalTarget(target string, exact bool, kind targetKind) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return target
	}

	alreadyExact := strings.HasPrefix(target, "=")
	raw := strings.TrimPrefix(target, "=")
	if isSpecialTarget(raw) {
		return target
	}

	session, suffix := splitTarget(raw)
	if kind == targetPane && suffix == "" {
		suffix = ":"
	}
	mapped := c.PhysicalSessionName(session) + suffix
	if exact || alreadyExact {
		return "=" + strings.TrimPrefix(mapped, "=")
	}
	return mapped
}

func splitTarget(target string) (string, string) {
	if idx := strings.Index(target, ":"); idx >= 0 {
		return target[:idx], target[idx:]
	}
	return target, ""
}

func isSpecialTarget(target string) bool {
	if target == "" {
		return true
	}
	switch target[0] {
	case '%', '@', '!', '$', '{', '.', '+', '-':
		return true
	default:
		return false
	}
}

func hasCompactFlag(arg string, flag byte) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) <= 2 {
		return false
	}
	return strings.ContainsRune(arg[1:], rune(flag))
}

func commandTargetKind(command string) (targetKind, bool) {
	switch canonicalCommand(command) {
	case "has-session", "attach-session", "kill-session", "set-option", "show-options":
		return targetSession, true
	case "paste-buffer", "send-keys", "capture-pane", "display-message":
		return targetPane, true
	default:
		return targetSession, false
	}
}

func isInteractive(command string) bool {
	switch canonicalCommand(command) {
	case "attach-session":
		return true
	default:
		return false
	}
}

func canonicalCommand(command string) string {
	switch command {
	case "attach":
		return "attach-session"
	case "new":
		return "new-session"
	case "has":
		return "has-session"
	case "kill-session", "kill":
		return "kill-session"
	case "capturep":
		return "capture-pane"
	case "display":
		return "display-message"
	case "set":
		return "set-option"
	case "show":
		return "show-options"
	case "ls":
		return "list-sessions"
	default:
		return command
	}
}

func shellJoin(parts []string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if isShellSafe(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func isShellSafe(s string) bool {
	if strings.HasPrefix(s, "=") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("@%_+=:,./-", r):
		default:
			return false
		}
	}
	return true
}

func (ExecRunner) Run(program string, args ...string) (string, error) {
	cmd := exec.Command(program, args...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimRight(string(out), "\n")
	if err != nil {
		return "", fmt.Errorf("%s %s: %s (%w)", program, strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return text, nil
}

func (ExecRunner) RunInteractive(program string, args ...string) error {
	cmd := exec.Command(program, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
