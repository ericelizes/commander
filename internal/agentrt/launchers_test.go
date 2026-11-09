package agentrt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// claudeLaunchers is every non-test Go file that starts a claude process or
// builds its command line, and how that child gets its account. A new file
// fails TestEveryClaudeLauncherIsAccounted until it is listed here, so its
// author decides what an inherited CLAUDE_CONFIG_DIR means for it.
var claudeLaunchers = map[string]string{
	"internal/agentrt/claude.go":    "ClaudeInteractiveCmd: always default, clears the variable; ClaudeHeadlessArgs: argv only, callers set the env",
	"internal/chat/engine.go":       "childEnv clears the variable; extra sets a named account (also the --version probe)",
	"internal/chat/handover.go":     "ClaudeInteractiveCmd",
	"internal/chat/host.go":         "h.cfg.Binary under os.Environ: the Engine starts the host with childEnv (TestChatHostRunsUnderChildEnv)",
	"internal/claudeacct/verify.go": "drops the variable, then Env(home, name)",
	"internal/inspector/digest.go":  "env -u CLAUDE_CONFIG_DIR",
	"internal/layout/layout.go":     "ClaudeInteractiveCmd through claudeChatCmd",
	"internal/overview/recap.go":    "cleanEnv drops the variable, then the selected account's",
	"internal/spawn/code.go":        "Docker: the container env is an allow-list without the variable",
	"internal/spawn/host.go":        "env -u CLAUDE_CONFIG_DIR, then claudeacct.Env(req.Account)",
	"internal/spawn/knowledge.go":   "env -u CLAUDE_CONFIG_DIR (legacy send-keys flow)",
}

// configDirReaders may read CLAUDE_CONFIG_DIR from the environment. The fake
// plays Claude itself; the carry only searches an inherited dir for a transcript
// a pane left there. Nothing else may follow an inherited value.
var configDirReaders = map[string]bool{"internal/chat/chattest/fakeclaude.go": true, "internal/claudeacct/carry.go": true}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}
		dir = parent
	}
}

func stringArg(e ast.Expr) string {
	if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		s, _ := strconv.Unquote(lit.Value)
		return s
	}
	return ""
}

// startsConfiguredBinary reports whether c is exec.Command[Context] of a *.Binary
// field: how the chat engine and host start claude.
func startsConfiguredBinary(c *ast.CallExpr) bool {
	fun, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || (fun.Sel.Name != "Command" && fun.Sel.Name != "CommandContext") {
		return false
	}
	for _, a := range c.Args {
		if s, ok := a.(*ast.SelectorExpr); ok && s.Sel.Name == "Binary" {
			return true
		}
	}
	return false
}

// launches reports whether n starts or builds a claude command: a call with the
// literal argument "claude", exec of a *.Binary field, an assignment of "claude"
// to a *Binary field, or a use of a command builder or the binary constant.
func launches(n ast.Node) bool {
	switch x := n.(type) {
	case *ast.CallExpr:
		for _, a := range x.Args {
			if stringArg(a) == "claude" {
				return true
			}
		}
		return startsConfiguredBinary(x)
	case *ast.AssignStmt:
		for i, l := range x.Lhs {
			if s, ok := l.(*ast.SelectorExpr); ok && strings.HasSuffix(s.Sel.Name, "Binary") && i < len(x.Rhs) && stringArg(x.Rhs[i]) == "claude" {
				return true
			}
		}
	case *ast.Ident:
		switch x.Name {
		case "ClaudeInteractiveCmd", "ClaudeHeadlessArgs", "ClaudeBinary":
			return true
		}
	}
	return false
}

func readsConfigDir(n ast.Node) bool {
	c, ok := n.(*ast.CallExpr)
	if !ok || len(c.Args) != 1 || stringArg(c.Args[0]) != "CLAUDE_CONFIG_DIR" {
		return false
	}
	s, ok := c.Fun.(*ast.SelectorExpr)
	return ok && (s.Sel.Name == "Getenv" || s.Sel.Name == "LookupEnv")
}

// Searches the Go sources (cmd, internal): default must mean ~/.claude in every
// child path, so a launcher nobody looked at must not appear unnoticed. A process
// started through a plain variable holding the name is out of reach of this
// search, and so is a reader scanning os.Environ.
func TestEveryClaudeLauncherIsAccounted(t *testing.T) {
	root := repoRoot(t)
	found := map[string]bool{}
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			ast.Inspect(file, func(n ast.Node) bool {
				if n == nil {
					return false
				}
				if launches(n) {
					found[rel] = true
				}
				if readsConfigDir(n) && !configDirReaders[rel] {
					t.Errorf("%s reads an inherited CLAUDE_CONFIG_DIR: use claudeacct.ConfigDir", rel)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var missing, stale []string
	for f := range found {
		if _, ok := claudeLaunchers[f]; !ok {
			missing = append(missing, f)
		}
	}
	for f := range claudeLaunchers {
		if !found[f] {
			stale = append(stale, f)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("new claude launchers: %v. Make a default child clear CLAUDE_CONFIG_DIR and read ~/.claude, then list them", missing)
	}
	if len(stale) > 0 {
		t.Errorf("listed files no longer launch claude: %v", stale)
	}
}

func isChildEnv(fun ast.Expr) bool {
	id, ok := fun.(*ast.Ident)
	return ok && id.Name == "childEnv"
}

// The chat host starts claude with its own environment (host.go), so the host's
// is what clears the variable: every exec in engine.go must run under childEnv.
func TestChatHostRunsUnderChildEnv(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "chat", "engine.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	envs := 0
	ast.Inspect(file, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 {
			return true
		}
		if l, ok := as.Lhs[0].(*ast.SelectorExpr); !ok || l.Sel.Name != "Env" {
			return true
		}
		envs++
		if call, ok := as.Rhs[0].(*ast.CallExpr); !ok || !isChildEnv(call.Fun) {
			t.Errorf("engine.go sets a child's Env without childEnv")
		}
		return true
	})
	// The --version probe and the chat host.
	if envs != 2 {
		t.Errorf("engine.go sets Env %d times, want 2: a child left to inherit keeps the variable", envs)
	}
	hostSrc, err := os.ReadFile(filepath.Join(root, "internal", "chat", "host.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hostSrc), "exec.Command(h.cfg.Binary, h.cfg.Args...)") || !strings.Contains(string(hostSrc), "cmd.Env = os.Environ()") {
		t.Errorf("host.go no longer starts claude under the environment the Engine gave it: check this test's premise")
	}
}
