package gitrepository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wio-platform/wio/internal/childprocess"
)

// Git reads configuration from every repository it runs in. A repository's
// own config can name programs to execute (core.fsmonitor, core.hooksPath,
// filter drivers, diff drivers, gpg.program via log.showSignature, ...), so
// running Git as the agent user in a repository somebody else controls lets
// that person execute code as the agent user.
//
// Wio therefore never passes safe.directory: Git's own ownership check stays
// in force and refuses repositories that do not belong to the agent user
// (unless an administrator explicitly trusts them in the agent's global
// config). On top of that:
//
//   - read-only inspection always neutralizes repository-configured programs,
//     whoever owns the repository;
//   - mutating commands keep today's behaviour (the owner's hooks, filters,
//     SSH command, ...) only for repositories owned by the agent user and are
//     hardened like inspection everywhere else.

type gitMode int

const (
	// gitInspect is for commands that only read repository state.
	gitInspect gitMode = iota
	// gitModify is for commands that change a repository and may rely on its
	// owner's hooks and filters (git-lfs, commit hooks, ...).
	gitModify
)

// currentUID is the user whose repositories are trusted. Tests replace it.
var currentUID = os.Geteuid

// inspectionConfig overrides repository settings that make Git start
// programs. Command-line configuration has the highest precedence, and Git
// passes it on to the Git processes it spawns (for example in submodules).
var inspectionConfig = []string{
	"core.fsmonitor=false",
	"core.hooksPath=/dev/null",
	"log.showSignature=false",
	// Refuse implicitly discovered bare repositories, such as a bare
	// repository committed inside a cloned workspace.
	"safe.bareRepository=explicit",
}

// filterFreeCommands never run clean/smudge/process filter drivers, so they
// can skip the extra config lookup that neutralizes them. Every other
// subcommand is assumed to read or write worktree content.
var filterFreeCommands = map[string]bool{
	"branch": true, "check-ref-format": true, "config": true, "fetch": true,
	"for-each-ref": true, "init": true, "log": true, "ls-remote": true,
	"push": true, "remote": true, "rev-parse": true, "show-ref": true,
	"symbolic-ref": true,
}

// RepositoryOwnedByCurrentUser reports whether the worktree root and its Git
// metadata belong to the effective user of this process.
func RepositoryOwnedByCurrentUser(worktree string) bool {
	return repositoryOwnedBy(worktree, currentUID())
}

// repositoryOwnedBy mirrors Git's ownership check: the worktree, the .git
// entry and, for a .git file, the Git directory it points to (and that
// directory's common directory) must all belong to uid.
func repositoryOwnedBy(worktree string, uid int) bool {
	if uid < 0 || worktree == "" {
		return false
	}
	info, err := os.Stat(worktree)
	if err != nil || !info.IsDir() || !ownedBy(info, uid) {
		return false
	}
	gitPath := filepath.Join(worktree, ".git")
	gitInfo, err := os.Lstat(gitPath)
	if err != nil || !ownedBy(gitInfo, uid) {
		return false
	}
	switch {
	case gitInfo.IsDir():
		return true
	case gitInfo.Mode().IsRegular():
		gitDir, err := readGitFile(gitPath)
		if err != nil || !ownedDirectory(gitDir, uid) {
			return false
		}
		common, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
		if errors.Is(err, os.ErrNotExist) {
			return true
		}
		if err != nil {
			return false
		}
		commonDir := strings.TrimSpace(string(common))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
		return ownedDirectory(commonDir, uid)
	case gitInfo.Mode()&os.ModeSymlink != 0:
		return ownedDirectory(gitPath, uid)
	default:
		return false
	}
}

func ownedBy(info os.FileInfo, uid int) bool {
	owner, ok := fileOwner(info)
	return ok && owner == uid
}

func ownedDirectory(path string, uid int) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir() && ownedBy(info, uid)
}

func readGitFile(gitPath string) (string, error) {
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return "", err
	}
	if len(data) > 4096 {
		return "", errors.New("git file is too large")
	}
	line, _, _ := strings.Cut(string(data), "\n")
	value, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "gitdir: ")
	if !ok || value == "" {
		return "", errors.New("git file is invalid")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(filepath.Dir(gitPath), value)
	}
	return value, nil
}

// InspectOutput runs a read-only Git command in directory with repository
// configured programs disabled and returns its standard output.
func InspectOutput(ctx context.Context, directory string, args ...string) ([]byte, error) {
	command, err := gitCommand(ctx, directory, gitInspect, args...)
	if err != nil {
		return nil, err
	}
	return childprocess.Output(command)
}

// RunInspect is InspectOutput returning combined output.
func RunInspect(ctx context.Context, directory string, args ...string) ([]byte, error) {
	return runGit(ctx, directory, args...)
}

// RunModify runs a Git command that changes the repository in directory. The
// repository's own hooks and filters only apply when the agent user owns it.
func RunModify(ctx context.Context, directory string, args ...string) ([]byte, error) {
	return runGitModify(ctx, directory, args...)
}

// runGit runs a read-only Git command and returns combined output.
func runGit(ctx context.Context, directory string, args ...string) ([]byte, error) {
	return runGitMode(ctx, directory, gitInspect, args...)
}

// runGitModify runs a Git command that changes the repository.
func runGitModify(ctx context.Context, directory string, args ...string) ([]byte, error) {
	return runGitMode(ctx, directory, gitModify, args...)
}

func runGitMode(ctx context.Context, directory string, mode gitMode, args ...string) ([]byte, error) {
	command, err := gitCommand(ctx, directory, mode, args...)
	if err != nil {
		return []byte(err.Error()), err
	}
	return childprocess.CombinedOutput(command)
}

func gitCommand(ctx context.Context, directory string, mode gitMode, args ...string) (*exec.Cmd, error) {
	commandArgs, err := gitArguments(ctx, directory, mode, args...)
	if err != nil {
		return nil, err
	}
	command := childprocess.CommandContext(ctx, "git", commandArgs...)
	command.Env = gitEnvironment()
	if directory == "" {
		// Commands without a repository (clone, ls-remote, check-ref-format)
		// must not pick up a repository from the agent's working directory.
		command.Dir = string(filepath.Separator)
	}
	return command, nil
}

func gitEnvironment() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
}

// gitArguments builds the global options for a Git command run in directory.
func gitArguments(ctx context.Context, directory string, mode gitMode, args ...string) ([]string, error) {
	if directory == "" {
		return append([]string(nil), args...), nil
	}
	commandArgs := make([]string, 0, len(args)+12)
	if mode == gitInspect || !RepositoryOwnedByCurrentUser(directory) {
		commandArgs = append(commandArgs, configArguments(inspectionConfig)...)
		if !filterFreeCommands[gitSubcommand(args)] {
			overrides, err := filterOverrides(ctx, directory)
			if err != nil {
				return nil, err
			}
			commandArgs = append(commandArgs, configArguments(overrides)...)
		}
	}
	commandArgs = append(commandArgs, "-C", directory)
	return append(commandArgs, args...), nil
}

func configArguments(values []string) []string {
	arguments := make([]string, 0, 2*len(values))
	for _, value := range values {
		arguments = append(arguments, "-c", value)
	}
	return arguments
}

// gitSubcommand returns the first argument that is not a global option.
func gitSubcommand(args []string) string {
	for index := 0; index < len(args); index++ {
		switch argument := args[index]; {
		case argument == "-c" || argument == "-C":
			index++
		case strings.HasPrefix(argument, "-"):
		default:
			return argument
		}
	}
	return ""
}

// filterOverrides disables every filter driver configured outside the
// protected system, global and command-line scopes. Filter drivers cannot be
// disabled with a wildcard, so the repository's drivers are listed first.
// An empty command makes Git skip the driver.
func filterOverrides(ctx context.Context, directory string) ([]string, error) {
	output, scoped, err := listFilterConfig(ctx, directory)
	if err != nil {
		return nil, err
	}
	return parseFilterOverrides(output, scoped)
}

func listFilterConfig(ctx context.Context, directory string) ([]byte, bool, error) {
	base := append(configArguments(inspectionConfig), "-C", directory, "config")
	query := []string{"--null", "--get-regexp", `^filter\.`}
	run := func(extra ...string) ([]byte, error) {
		args := append(append(append([]string(nil), base...), extra...), query...)
		command := childprocess.CommandContext(ctx, "git", args...)
		command.Env = gitEnvironment()
		return childprocess.Output(command)
	}
	output, err := run("--show-scope")
	if err == nil {
		return output, true, nil
	}
	if noMatches(err) {
		return nil, true, nil
	}
	// Git before 2.26 has no --show-scope: neutralize drivers from every scope.
	output, err = run()
	if err == nil {
		return output, false, nil
	}
	if noMatches(err) {
		return nil, false, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(bytes.TrimSpace(exitErr.Stderr)) > 0 {
		return nil, false, fmt.Errorf("inspect repository filter drivers: %s", truncateMessage(exitErr.Stderr))
	}
	return nil, false, fmt.Errorf("inspect repository filter drivers: %w", err)
}

func truncateMessage(message []byte) string {
	message = bytes.TrimSpace(message)
	if len(message) > 4096 {
		message = message[:4096]
	}
	return string(message)
}

// noMatches recognizes git config's exit status for "no matching key".
func noMatches(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

func parseFilterOverrides(output []byte, scoped bool) ([]string, error) {
	fields := bytes.Split(output, []byte{0})
	seen := make(map[string]bool)
	var overrides []string
	for index := 0; index < len(fields); index++ {
		scope := ""
		if scoped {
			scope = string(fields[index])
			if scope == "" && index == len(fields)-1 {
				break
			}
			index++
			if index >= len(fields) {
				return nil, errors.New("inspect repository filter drivers: unexpected Git output")
			}
		}
		entry := fields[index]
		if len(entry) == 0 {
			if index == len(fields)-1 {
				break
			}
			return nil, errors.New("inspect repository filter drivers: unexpected Git output")
		}
		key, _, _ := strings.Cut(string(entry), "\n")
		if scope == "system" || scope == "global" || scope == "command" {
			continue
		}
		name, ok := strings.CutPrefix(key, "filter.")
		if !ok {
			continue
		}
		last := strings.LastIndexByte(name, '.')
		if last <= 0 {
			// filter.<variable> without a driver name is not a driver.
			continue
		}
		name = name[:last]
		if seen[name] {
			continue
		}
		if strings.ContainsAny(name, "=\n\x00") {
			return nil, fmt.Errorf("repository filter driver %q cannot be disabled safely", name)
		}
		seen[name] = true
		prefix := "filter." + name + "."
		overrides = append(overrides, prefix+"clean=", prefix+"smudge=", prefix+"process=", prefix+"required=false")
	}
	return overrides, nil
}
