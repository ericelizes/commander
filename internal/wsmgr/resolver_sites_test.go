package wsmgr

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// meansTheTop lists every call that resolves a bead to the TOP of its chain
// (beads.Root, wsmgr.StrictRoot) on purpose. Routing and authority go through the
// owner resolvers (beads.Owner, wsmgr.Owner, StrictOwner, OwnerChain); a new
// root call fails here until it is added with why it means the top, so two
// resolvers never disagree about who owns a bead.
var meansTheTop = map[string]string{
	"cmd/cmdr/internal_gate_retire_legacy.go:gateRetireLegacy": "the audit record keeps the root it always named",
	"cmd/cmdr/internal_gate_unlink.go:checkObsoleteWaiter":     "unlink clears an edge inside one top-level workstream",
	"cmd/cmdr/internal_gate_unlink.go:gateWorkstream":          "the same-workstream check compares tops",
	"internal/livedata/owner.go:ownerByParents":                "fallback for a bead with no loaded row",
	"internal/api/tabs.go:rootBead":                            "fallback with no loaded data",
	"internal/daemon/watch.go:pass":                            "fallback when the snapshot has no grouping",
}

func TestEveryRootResolverIsAccounted(t *testing.T) {
	repo := filepath.Join("..", "..")
	found := map[string]bool{}
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repo, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(repo, path)
			rel = filepath.ToSlash(rel)
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					name := ""
					switch f := call.Fun.(type) {
					case *ast.SelectorExpr:
						if x, ok := f.X.(*ast.Ident); ok {
							name = x.Name + "." + f.Sel.Name
						}
					case *ast.Ident:
						// Inside the defining packages.
						if strings.HasPrefix(rel, "internal/wsmgr/") && f.Name == "StrictRoot" {
							name = "wsmgr.StrictRoot"
						}
						if strings.HasPrefix(rel, "internal/beads/") && f.Name == "Root" {
							name = "beads.Root"
						}
					}
					if name == "beads.Root" || name == "wsmgr.StrictRoot" {
						found[rel+":"+fn.Name.Name] = true
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var unlisted []string
	for site := range found {
		if _, ok := meansTheTop[site]; !ok {
			unlisted = append(unlisted, site)
		}
	}
	sort.Strings(unlisted)
	if len(unlisted) > 0 {
		t.Fatalf("calls that resolve to the root without a reason (use beads.Owner / wsmgr.Owner / StrictOwner / OwnerChain, or list why the top is meant):\n%s", strings.Join(unlisted, "\n"))
	}
	for site := range meansTheTop {
		if !found[site] {
			t.Errorf("stale allow-list entry %s: no root call there any more", site)
		}
	}
}
