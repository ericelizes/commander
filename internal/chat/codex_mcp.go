package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// These paths are link-injectable only for throwaway fixture binaries; production
// has no environment switch that bypasses system or managed policy.
// Legacy managed config follows session flags in Codex; an explicit enabled=true
// there cannot be defeated by a launch override and is refused below.
var codexSystemConfigPath = "/etc/codex/config.toml"
var codexManagedConfigPath = "/etc/codex/managed_config.toml"

// codexConfigPaths covers file-backed configuration in precedence order.
func codexConfigPaths(home, cwd string) ([]string, error) {
	return codexConfigPathsFrom(codexSystemConfigPath, filepath.Join(home, "config.toml"), codexManagedConfigPath, cwd)
}

// Inject the system/managed locations in tests; never redirect or edit real config.
// Codex 0.164 merges system, user, then managed before deciding project trust.
func codexConfigPathsFrom(system, user, managed, cwd string) ([]string, error) {
	paths := []string{system, user}
	projects := map[string]string{}
	markers := []string{".git"}
	for _, path := range []string{system, user, managed} {
		var layer struct {
			Markers  *[]string `toml:"project_root_markers"`
			Projects map[string]struct {
				Trust *string `toml:"trust_level"`
			} `toml:"projects"`
		}
		if _, err := toml.DecodeFile(path, &layer); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("cannot read Codex trust configuration %s", path)
		}
		if layer.Markers != nil {
			markers = *layer.Markers
		}
		for dir, project := range layer.Projects {
			if project.Trust != nil {
				if *project.Trust != "trusted" && *project.Trust != "untrusted" {
					return nil, fmt.Errorf("invalid Codex project trust in %s", path)
				}
				projects[dir] = *project.Trust
			}
		}
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("cannot determine Codex working directory")
		}
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve Codex working directory")
	}
	// Keep the absolute raw spelling: Codex walks cwd.ancestors(), even when
	// a symlink points into a different directory tree. Canonicalize lookup
	// keys, not discovery paths (rust-v0.164.0 config/src/loader/mod.rs).
	var dirs []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if filepath.Dir(dir) == dir {
			break
		}
	}
	// Discovery stops at the nearest configured project marker (cwd if none).
	root := cwd
findRoot:
	for _, dir := range dirs {
		for _, marker := range markers {
			markerPath := filepath.Join(dir, marker)
			if info, err := os.Stat(markerPath); err == nil {
				// rust-v0.164.0 loader::discover_project_root follows stat:
				// .git files qualify directly; directories require a stat-able HEAD.
				if marker == ".git" && info.IsDir() {
					if _, err := os.Stat(filepath.Join(markerPath, "HEAD")); err != nil {
						continue
					}
				}
				root = dir
				break findRoot
			}
		}
	}
	repoRoot := codexRepoTrustRoot(dirs)
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		if rel, err := filepath.Rel(root, dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		trust := codexProjectTrust(projects, dir)
		if trust == "" {
			trust = codexProjectTrust(projects, root)
		}
		if trust == "" {
			trust = codexProjectTrust(projects, repoRoot)
		}
		if trust == "trusted" {
			paths = append(paths, filepath.Join(dir, ".codex", "config.toml"))
		}
	}
	return append(paths, managed), nil
}

// Canonical spelling wins over raw spelling for each fallback in order:
// directory, project root, then main repository root. Do not normalize stored
// keys: Codex normalizes the lookup, retaining raw keys as a second choice.
func codexProjectTrust(projects map[string]string, dir string) string {
	if dir == "" {
		return ""
	}
	if canonical, err := filepath.EvalSymlinks(dir); err == nil {
		if trust := projects[canonical]; trust != "" {
			return trust
		}
	}
	return projects[dir]
}

// Codex also falls back to the main repository trust key for linked worktrees.
// Validate the reciprocal registration rather than trusting an arbitrary gitdir
// pointer. No git command, agent, or browser is started.
func codexRepoTrustRoot(dirs []string) string {
	for _, dir := range dirs {
		dotGit := filepath.Join(dir, ".git")
		info, err := os.Stat(dotGit)
		if err != nil {
			continue
		}
		if info.IsDir() {
			if _, err := os.Stat(filepath.Join(dotGit, "HEAD")); err == nil {
				return dir
			}
			continue
		}
		pointer, ok := codexGitMetadata(dotGit)
		if !ok || !strings.HasPrefix(pointer, "gitdir:") {
			return ""
		}
		gitDir := codexRelativePath(dir, strings.TrimSpace(strings.TrimPrefix(pointer, "gitdir:")))
		info, err = os.Lstat(gitDir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ""
		}
		canonical, err := filepath.EvalSymlinks(gitDir)
		if err != nil || filepath.Base(filepath.Dir(canonical)) != "worktrees" {
			return ""
		}
		common := filepath.Dir(filepath.Dir(canonical))
		registered, ok := codexGitMetadata(filepath.Join(canonical, "gitdir"))
		if !ok || filepath.Base(registered) != ".git" {
			return ""
		}
		checkout, err := filepath.EvalSymlinks(filepath.Dir(codexRelativePath(canonical, registered)))
		dirCanonical, dirErr := filepath.EvalSymlinks(dir)
		if err != nil || dirErr != nil || checkout != dirCanonical {
			return ""
		}
		linked, ok := codexGitMetadata(filepath.Join(canonical, "commondir"))
		if !ok {
			return ""
		}
		linked, err = filepath.EvalSymlinks(codexRelativePath(canonical, linked))
		if err != nil || linked != common {
			return ""
		}
		main := filepath.Dir(filepath.Dir(gitDir))
		main = filepath.Dir(main)
		mainGit := filepath.Join(main, ".git")
		if pointer, ok := codexGitMetadata(mainGit); ok && strings.HasPrefix(pointer, "gitdir:") {
			mainGit = codexRelativePath(main, strings.TrimSpace(strings.TrimPrefix(pointer, "gitdir:")))
		}
		mainGit, err = filepath.EvalSymlinks(mainGit)
		if err != nil || mainGit != common {
			return ""
		}
		return main
	}
	return ""
}

func codexRelativePath(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(base, path)
}

func codexGitMetadata(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return "", false
	}
	data, err := os.ReadFile(path)
	value := strings.TrimSpace(string(data))
	return value, err == nil && len(data) <= 64*1024 && value != ""
}

// CodexMCPLaunchArgsFromConfig discovers project layers using effective trust,
// then applies launch policy. Explicit locations allow isolated verification.
func CodexMCPLaunchArgsFromConfig(system, user, managed, cwd, browserServer string) ([]string, error) {
	paths, err := codexConfigPathsFrom(system, user, managed, cwd)
	if err != nil {
		return nil, err
	}
	return CodexMCPLaunchArgs(paths, browserServer)
}

// CodexMCPLaunchArgs generates app-server argv from file-backed config layers
// (lowest precedence first). Ordinary roles load no chrome-devtools MCP; an
// explicitly browser-required launch may select exactly one by name. Other MCPs
// are preserved, except agent-mail which Commander has always disabled.
// It never starts Codex, an MCP, or a browser, and never modifies configuration.
func CodexMCPLaunchArgs(paths []string, browserServer string) ([]string, error) {
	servers := map[string]map[string]any{}
	managedEnabled := map[string]bool{}
	for _, path := range paths {
		var layer struct {
			Servers map[string]map[string]any `toml:"mcp_servers"`
		}
		if _, err := toml.DecodeFile(path, &layer); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			// Do not echo parser errors: they can contain credential-bearing TOML lines.
			return nil, fmt.Errorf("cannot read Codex MCP configuration %s", path)
		}
		for name, fields := range layer.Servers {
			if servers[name] == nil {
				servers[name] = map[string]any{}
			}
			for key, value := range fields {
				servers[name][key] = value
			}
			if filepath.Base(path) == "managed_config.toml" {
				if enabled, ok := fields["enabled"].(bool); ok {
					managedEnabled[name] = enabled
				}
			}
		}
	}
	var names []string
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	found := browserServer == ""
	args := []string{"app-server"}
	for _, name := range names {
		fields := servers[name]
		browser := strings.Contains(strings.ToLower(name), "chrome") && strings.Contains(strings.ToLower(name), "devtools")
		if command, ok := fields["command"].(string); ok {
			browser = browser || strings.Contains(command, "chrome-devtools")
		}
		if values, ok := fields["args"].([]any); ok {
			for _, value := range values {
				if arg, ok := value.(string); ok {
					browser = browser || strings.Contains(arg, "chrome-devtools")
				}
			}
		}
		disabled := false
		for _, mail := range codexAgentMailServers {
			disabled = disabled || name == mail
		}
		if browser {
			// Codex 0.164 splits override paths on literal dots and does not
			// decode quoted segments. Only bare ASCII aliases are safe.
			if !codexBareMCPKey.MatchString(name) {
				return nil, fmt.Errorf("browser MCP %q cannot be represented safely in a Codex override path; rename the alias using only ASCII letters, digits, underscores or hyphens in its configuration", name)
			}
			disabled = disabled || name != browserServer
			if name == browserServer && disabled {
				return nil, fmt.Errorf("agent-mail MCP %q cannot be selected as a browser", name)
			}
			if !disabled {
				if enabled, ok := fields["enabled"].(bool); ok && !enabled {
					return nil, fmt.Errorf("selected browser MCP %q is disabled in configuration", name)
				}
				found = true
			}
		}
		if disabled {
			if managedEnabled[name] {
				return nil, fmt.Errorf("managed configuration forces MCP %q enabled; cannot enforce launch policy; ask your administrator to remove the forced enabled=true setting from managed_config.toml", name)
			}
			// Policy targets have bare aliases; quoted override segments are unsafe.
			args = append(args, "-c", "mcp_servers."+name+".enabled=false")
		}
	}
	if !found {
		return nil, fmt.Errorf("selected browser MCP %q is not configured as chrome-devtools", browserServer)
	}
	return args, nil
}

var codexBareMCPKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func codexMCPKey(name string) string {
	if codexBareMCPKey.MatchString(name) {
		return name
	}
	return strconv.Quote(name)
}
