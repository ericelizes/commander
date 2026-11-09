package chat

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCodexMCPLaunchPolicy(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "config.toml")
	managed := filepath.Join(dir, "managed_config.toml")
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(user, `[mcp_servers.mcp_agent_mail]
url = 'http://example.invalid/mail'
[mcp_servers.search]
command = 'fake-search'
[mcp_servers."browser-alias"]
command = 'node'
args = ['chrome-devtools-mcp']
`)
	write(managed, `[mcp_servers.vm-browser]
command = 'node'
args = ['/fake/chrome-devtools-mcp', '--browser-url=http://example.invalid']
[mcp_servers.remote-browser]
command = '/fake/chrome-devtools-mcp'
`)
	paths := []string{user, managed}
	for _, selected := range []string{"", "vm-browser", "remote-browser", "browser-alias"} {
		args, err := CodexMCPLaunchArgs(paths, selected)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(args, " ")
		if strings.Contains(got, "search") {
			t.Fatalf("other MCP changed: %s", got)
		}
		for _, name := range []string{"mcp_agent_mail", "vm-browser", "remote-browser", "browser-alias"} {
			wantDisabled := name != selected
			if strings.Contains(got, "mcp_servers."+codexMCPKey(name)+".enabled=false") != wantDisabled {
				t.Fatalf("selected %q: %s", selected, got)
			}
		}
	}
	if _, err := CodexMCPLaunchArgs(paths, "missing"); err == nil {
		t.Fatal("unknown browser accepted")
	}
	write(managed, `[mcp_servers.vm-browser]
command = 'chrome-devtools-mcp'
enabled = true
`)
	if _, err := CodexMCPLaunchArgs(paths, ""); err == nil || !strings.Contains(err.Error(), "vm-browser") || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("managed conflict lacks actionable explanation: %v", err)
	}
	write(managed, `[mcp_servers.vm-browser]
command = 'chrome-devtools-mcp'
enabled = false
`)
	if _, err := CodexMCPLaunchArgs(paths, "vm-browser"); err == nil {
		t.Fatal("disabled browser selected")
	}
	write(managed, "secret-bearing = !!!")
	if _, err := CodexMCPLaunchArgs(paths, ""); err == nil || strings.Contains(err.Error(), "secret-bearing") {
		t.Fatalf("unsafe parse error: %v", err)
	}
}

func TestCodexMCPConfigPathsAndOptIn(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	cwd := filepath.Join(dir, "repo", "child")
	for _, p := range []string{home, cwd, filepath.Join(filepath.Dir(cwd), ".git")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeCodexFixtureHEAD(t, filepath.Join(filepath.Dir(cwd), ".git"))
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[projects."+codexMCPKey(filepath.Dir(cwd))+"]\ntrust_level = 'trusted'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	paths, err := codexConfigPathsFrom(filepath.Join(dir, "absent-system"), filepath.Join(home, "config.toml"), filepath.Join(dir, "managed_config.toml"), cwd)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(paths, "\n")
	if !strings.Contains(got, filepath.Join(filepath.Dir(cwd), ".codex", "config.toml")) || !strings.HasSuffix(got, filepath.Join(dir, "managed_config.toml")) {
		t.Fatal(got)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	paths, err = codexConfigPathsFrom(filepath.Join(dir, "absent-system"), filepath.Join(home, "config.toml"), filepath.Join(dir, "managed_config.toml"), cwd)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(paths, "\n"); strings.Contains(got, filepath.Join(dir, "repo", ".codex")) {
		t.Fatal("untrusted project config loaded")
	}
	args := strings.Join((CodexBridgeConfig{BrowserServer: "vm-browser"}).Args(), " ")
	if !strings.Contains(args, "--browser-server vm-browser") {
		t.Fatal(args)
	}
}

// All files here are synthetic, including the system and managed layers.
func TestCodexMCPEffectiveTrustDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, system, user, managed string
		want                        bool
		bad                         bool
	}{
		{name: "system only", system: "trusted", want: true},
		{name: "managed only", managed: "trusted", want: true},
		{name: "user overrides system", system: "trusted", user: "untrusted"},
		{name: "managed overrides user", user: "untrusted", managed: "trusted", want: true},
		{name: "managed distrust wins", user: "trusted", managed: "untrusted"},
		{name: "unknown"},
		{name: "explicit untrusted", user: "untrusted"},
		{name: "invalid trust", managed: "secret-invalid", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cwd := filepath.Join(dir, "repo", "child")
			project := filepath.Join(filepath.Dir(cwd), ".codex", "config.toml")
			if err := os.MkdirAll(filepath.Dir(project), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(filepath.Dir(cwd), ".git"), 0700); err != nil {
				t.Fatal(err)
			}
			writeCodexFixtureHEAD(t, filepath.Join(filepath.Dir(cwd), ".git"))
			if err := os.WriteFile(project, []byte("[mcp_servers.project-browser]\ncommand='chrome-devtools-mcp'\n[mcp_servers.search]\ncommand='fake-search'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			layers := []string{filepath.Join(dir, "system.toml"), filepath.Join(dir, "user.toml"), filepath.Join(dir, "managed_config.toml")}
			before := map[string]string{}
			for i, trust := range []string{tc.system, tc.user, tc.managed} {
				if trust == "" {
					continue
				}
				content := "[projects." + codexMCPKey(filepath.Dir(cwd)) + "]\ntrust_level='" + trust + "'\n"
				before[layers[i]] = content
				if err := os.WriteFile(layers[i], []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			paths, err := codexConfigPathsFrom(layers[0], layers[1], layers[2], cwd)
			if tc.bad {
				if err == nil || strings.Contains(err.Error(), "secret-invalid") {
					t.Fatalf("unsafe trust error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			args, err := CodexMCPLaunchArgs(paths, "")
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(args, " ")
			if strings.Contains(got, "mcp_servers.project-browser.enabled=false") != tc.want {
				t.Fatalf("discovery mismatch: %s", got)
			}
			if strings.Contains(got, "search") {
				t.Fatalf("nonbrowser changed: %s", got)
			}
			if tc.want {
				args, err = CodexMCPLaunchArgs(paths, "project-browser")
				if err != nil || len(args) != 1 {
					t.Fatalf("explicit selection: %v %v", args, err)
				}
			}
			for path, want := range before {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("mutated %s", path)
				}
			}
		})
	}
}

func TestCodexMCPMalformedTrustConfigIsRedacted(t *testing.T) {
	for _, layer := range []int{0, 1, 2} {
		t.Run([]string{"system", "user", "managed"}[layer], func(t *testing.T) {
			dir := t.TempDir()
			paths := []string{filepath.Join(dir, "system"), filepath.Join(dir, "user"), filepath.Join(dir, "managed")}
			if err := os.WriteFile(paths[layer], []byte("credential-secret = !!!"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := codexConfigPathsFrom(paths[0], paths[1], paths[2], dir)
			if err == nil || strings.Contains(err.Error(), "credential-secret") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestCodexMCPProjectDiscoveryBoundaries(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	cwd := filepath.Join(repo, "child")
	for _, p := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".codex"), filepath.Join(cwd, ".codex"), filepath.Join(dir, ".codex")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeCodexFixtureHEAD(t, filepath.Join(repo, ".git"))
	system, user, managed := filepath.Join(dir, "system.toml"), filepath.Join(dir, "user.toml"), filepath.Join(dir, "managed_config.toml")
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	trust := func(p, v string) string { return "[projects." + codexMCPKey(p) + "]\ntrust_level='" + v + "'\n" }
	write(system, trust(repo, "trusted"))
	write(managed, trust(cwd, "untrusted"))
	write(filepath.Join(dir, ".codex", "config.toml"), "outside-secret = !!!")
	write(filepath.Join(repo, ".codex", "config.toml"), "[mcp_servers.root-browser]\ncommand='chrome-devtools-mcp'\n")
	write(filepath.Join(cwd, ".codex", "config.toml"), "untrusted-secret = !!!")
	args, err := CodexMCPLaunchArgsFromConfig(system, user, managed, cwd, "")
	if err != nil || !strings.Contains(strings.Join(args, " "), "root-browser.enabled=false") {
		t.Fatalf("%v %v", args, err)
	}
	write(managed, trust(cwd, "trusted"))
	if _, err := CodexMCPLaunchArgsFromConfig(system, user, managed, cwd, ""); err == nil || strings.Contains(err.Error(), "untrusted-secret") {
		t.Fatalf("unsafe trusted-project parse error: %v", err)
	}
	// An unrelated trusted parent does not authorize a directory with no marker.
	write(system, "project_root_markers=[]\n"+trust(dir, "trusted"))
	write(managed, "")
	if err := os.Remove(filepath.Join(repo, ".git", "HEAD")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}
	args, err = CodexMCPLaunchArgsFromConfig(system, user, managed, cwd, "")
	if err != nil || len(args) != 1 {
		t.Fatalf("unknown project loaded: %v %v", args, err)
	}
	// Custom markers from managed configuration win over user configuration.
	write(user, "project_root_markers=['missing-marker']\n")
	write(filepath.Join(repo, "project-marker"), "")
	write(managed, "project_root_markers=['project-marker']\n"+trust(repo, "trusted")+trust(cwd, "untrusted"))
	args, err = CodexMCPLaunchArgsFromConfig(system, user, managed, cwd, "")
	if err != nil || !strings.Contains(strings.Join(args, " "), "root-browser.enabled=false") {
		t.Fatalf("custom marker: %v %v", args, err)
	}
}

func TestCodexMCPManagedWorktreeTrust(t *testing.T) {
	for _, spelling := range []string{"raw", "canonical", "canonical denial"} {
		t.Run(spelling, func(t *testing.T) {
			dir := t.TempDir()
			alias := filepath.Join(dir, "alias")
			if err := os.Symlink(dir, alias); err != nil {
				t.Fatal(err)
			}
			dir = alias
			main := filepath.Join(dir, "main")
			checkout := filepath.Join(dir, "checkout")
			metadata := filepath.Join(main, ".git", "worktrees", "fixture")
			for _, p := range []string{metadata, filepath.Join(checkout, ".codex")} {
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(p, s string) {
				t.Helper()
				if err := os.WriteFile(p, []byte(s), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeCodexFixtureHEAD(t, filepath.Join(main, ".git"))
			writeCodexFixtureHEAD(t, metadata)
			write(filepath.Join(checkout, ".git"), "gitdir: "+metadata+"\n")
			write(filepath.Join(metadata, "gitdir"), filepath.Join(checkout, ".git")+"\n")
			write(filepath.Join(metadata, "commondir"), "../..\n")
			write(filepath.Join(checkout, ".codex", "config.toml"), "[mcp_servers.worktree-browser]\ncommand='chrome-devtools-mcp'\n")
			managed := filepath.Join(dir, "managed_config.toml")
			key := main
			canonical, err := filepath.EvalSymlinks(main)
			if err != nil {
				t.Fatal(err)
			}
			if spelling == "canonical" {
				key = canonical
			}
			trust := "[projects." + codexMCPKey(key) + "]\ntrust_level='trusted'\n"
			if spelling == "canonical denial" {
				trust += "[projects." + codexMCPKey(canonical) + "]\ntrust_level='untrusted'\n"
			}
			write(managed, trust)
			args, err := CodexMCPLaunchArgsFromConfig(filepath.Join(dir, "missing-system"), filepath.Join(dir, "missing-user"), managed, checkout, "")
			if err != nil || strings.Contains(strings.Join(args, " "), "worktree-browser.enabled=false") != (spelling != "canonical denial") {
				t.Fatalf("%v %v", args, err)
			}
			write(filepath.Join(metadata, "gitdir"), filepath.Join(dir, "unrelated", ".git"))
			args, err = CodexMCPLaunchArgsFromConfig(filepath.Join(dir, "missing-system"), filepath.Join(dir, "missing-user"), managed, checkout, "")
			if err != nil || len(args) != 1 {
				t.Fatalf("invalid registration trusted: %v %v", args, err)
			}
		})
	}
}

// The alias's ancestors differ from the target's: walking canonical ancestors
// would load the target parent's config, and lose trust stored under raw keys.
func TestCodexMCPSymlinkTrust(t *testing.T) {
	for _, layer := range []string{"system", "managed"} {
		for _, tc := range []struct {
			name, rawRoot, canonicalRoot, rawChild, canonicalChild string
			root, child                                            bool
		}{
			{name: "raw root", rawRoot: "trusted", root: true, child: true},
			{name: "canonical root", canonicalRoot: "trusted", root: true, child: true},
			{name: "canonical denial wins", rawRoot: "trusted", canonicalRoot: "untrusted"},
			{name: "canonical trust wins", rawRoot: "untrusted", canonicalRoot: "trusted", root: true, child: true},
			{name: "raw child denial", rawRoot: "trusted", rawChild: "untrusted", root: true},
			{name: "canonical child denial", rawRoot: "trusted", rawChild: "trusted", canonicalChild: "untrusted", root: true},
			{name: "raw directory trust", rawChild: "trusted", child: true},
			{name: "canonical directory trust", canonicalChild: "trusted", child: true},
		} {
			t.Run(layer+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				targetParent := filepath.Join(dir, "target")
				repo := filepath.Join(targetParent, "repo")
				aliasParent := filepath.Join(dir, "alias")
				for _, p := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".codex"), filepath.Join(repo, "child", ".codex"), aliasParent, filepath.Join(targetParent, ".codex")} {
					if err := os.MkdirAll(p, 0700); err != nil {
						t.Fatal(err)
					}
				}
				writeCodexFixtureHEAD(t, filepath.Join(repo, ".git"))
				alias := filepath.Join(aliasParent, "repo")
				if err := os.Symlink(repo, alias); err != nil {
					t.Fatal(err)
				}
				canonical, err := filepath.EvalSymlinks(repo)
				if err != nil {
					t.Fatal(err)
				}
				write := func(p, s string) {
					t.Helper()
					if err := os.WriteFile(p, []byte(s), 0600); err != nil {
						t.Fatal(err)
					}
				}
				write(filepath.Join(repo, ".codex", "config.toml"), "[mcp_servers.root-browser]\ncommand='chrome-devtools-mcp'\n")
				write(filepath.Join(repo, "child", ".codex", "config.toml"), "[mcp_servers.child-browser]\ncommand='chrome-devtools-mcp'\n")
				// Neither physical nor raw ancestors outside the project root load.
				write(filepath.Join(targetParent, ".codex", "config.toml"), "outside-secret = !!!")
				if err := os.MkdirAll(filepath.Join(aliasParent, ".codex"), 0700); err != nil {
					t.Fatal(err)
				}
				write(filepath.Join(aliasParent, ".codex", "config.toml"), "outside-secret = !!!")
				var trust strings.Builder
				for _, pair := range [][2]string{{alias, tc.rawRoot}, {canonical, tc.canonicalRoot}, {filepath.Join(alias, "child"), tc.rawChild}, {filepath.Join(canonical, "child"), tc.canonicalChild}, {aliasParent, "trusted"}, {targetParent, "trusted"}} {
					if pair[1] != "" {
						trust.WriteString("[projects." + codexMCPKey(pair[0]) + "]\ntrust_level='" + pair[1] + "'\n")
					}
				}
				system, user, managed := filepath.Join(dir, "system.toml"), filepath.Join(dir, "user.toml"), filepath.Join(dir, "managed_config.toml")
				path := system
				if layer == "managed" {
					path = managed
				}
				write(path, trust.String())
				args, err := CodexMCPLaunchArgsFromConfig(system, user, managed, filepath.Join(alias, "child"), "")
				if err != nil {
					t.Fatal(err)
				}
				for name, want := range map[string]bool{"root-browser": tc.root, "child-browser": tc.child} {
					if strings.Contains(strings.Join(args, " "), name+".enabled=false") != want {
						t.Fatalf("%s: %v", name, args)
					}
				}
			})
		}
	}
}

// Reject even selected or already disabled unsafe browsers: every discovered
// browser must have an enforceable override path. Other MCP aliases stay intact.
func TestCodexMCPUnsafeBrowserAlias(t *testing.T) {
	for _, name := range []string{"browser.alias", "browser alias", "browser\"alias", "bröwser", "", "browser\\alias"} {
		t.Run(strconv.Quote(name), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			content := "[mcp_servers." + strconv.Quote(name) + "]\ncommand='chrome-devtools-mcp'\nenabled=false\n"
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			for _, selected := range []string{"", name, "safe-browser"} {
				args, err := CodexMCPLaunchArgs([]string{path}, selected)
				if err == nil || args != nil || !strings.Contains(err.Error(), strconv.Quote(name)) || !strings.Contains(err.Error(), "rename") {
					t.Fatalf("args=%v err=%v", args, err)
				}
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != content {
				t.Fatal("config mutated", err)
			}
			content = strings.Replace(content, "chrome-devtools-mcp", "fake-search", 1)
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if args, err := CodexMCPLaunchArgs([]string{path}, ""); err != nil || len(args) != 1 {
				t.Fatalf("nonbrowser args=%v err=%v", args, err)
			}
		})
	}
}

func writeCodexFixtureHEAD(t *testing.T, gitDir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

// Match pinned discovery's stat behavior, including gitfiles and custom markers.
func TestCodexMCPHeadlessNestedMarker(t *testing.T) {
	for _, kind := range []string{"headless directory", "directory with HEAD", "gitfile", "symlink directory", "custom directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			repo := filepath.Join(dir, "repo")
			nested := filepath.Join(repo, "nested")
			cwd := filepath.Join(nested, "child")
			for _, p := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".codex"), cwd} {
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			writeCodexFixtureHEAD(t, filepath.Join(repo, ".git"))
			marker := filepath.Join(nested, ".git")
			switch kind {
			case "gitfile":
				if err := os.WriteFile(marker, []byte("gitdir: absent\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink directory":
				if err := os.Symlink(filepath.Join(repo, ".git"), marker); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Mkdir(marker, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "directory with HEAD" {
					writeCodexFixtureHEAD(t, marker)
				}
			}
			config := "[projects." + codexMCPKey(repo) + "]\ntrust_level='trusted'\n"
			if kind == "custom directory" {
				config = "project_root_markers=['.git','custom']\n" + config
				if err := os.Mkdir(filepath.Join(nested, "custom"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			system := filepath.Join(dir, "system.toml")
			if err := os.WriteFile(system, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, ".codex", "config.toml"), []byte("[mcp_servers.parent-browser]\ncommand='chrome-devtools-mcp'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			args, err := CodexMCPLaunchArgsFromConfig(system, filepath.Join(dir, "user.toml"), filepath.Join(dir, "managed_config.toml"), cwd, "")
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(strings.Join(args, " "), "parent-browser.enabled=false"); got != (kind == "headless directory") {
				t.Fatalf("parent discovery: %v", args)
			}
		})
	}
}
