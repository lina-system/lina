// SPDX-License-Identifier: Apache-2.0
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const rootModule = "github.com/thisisjun786/lina"

// C-40 assigns package families, including their subpackages, to these layers.
var layers = func() map[string]int {
	result := make(map[string]int)
	for i, names := range []string{
		"canon eventstore secrets ctlfiles guard ids",
		"authority lifecycle budget manifest binding toolcatalog recall",
		"ledger inbox agents memory retract skills records assets work timers plugins channels accounts",
		"a2a lane assemble compaction turn runs replay intakereplay reply results modelpath localmodel exec exchange gateway worker reactor review ghclient progress waiting backup update recover clientsrv jobs mcphost install miniapps",
		"boot supervise",
	} {
		for _, name := range strings.Fields(names) {
			result[name] = i + 1
		}
	}
	return result
}()

// C-35 names the kit packages Node can use; C-02 also permits the standard library.
var nodeKit = map[string]bool{
	"grant": true, "lease": true, "sandbox": true, "proc": true,
	"protocol": true, "rpc": true, "diag": true, "secret": true,
}

// Worker layers are separate from Core: wire contract, read-only canon,
// job runtime, then the command. Only the reader may import SQLite.
func moiraiLayer(name string) int {
	switch {
	case within(name, "cmd/moirai-worker"):
		return 4
	case name == "internal/moirai":
		return 3
	case within(name, "internal/moirai/planning"):
		return 2
	case within(name, "internal/moirai/canon"):
		return 2
	case within(name, "internal/moirai/jobapi"):
		return 1
	}
	return 0
}

func within(name, prefix string) bool {
	return name == prefix || strings.HasPrefix(name, prefix+"/")
}

func family(name, prefix string) string {
	return strings.Split(strings.TrimPrefix(name, prefix+"/"), "/")[0]
}

// violations checks production sources under root against the C-40 import rules.
// Parse every build variant: a Linux run must also catch a forbidden Windows import.
// Test harnesses and fixture/vendor trees are not LINA production packages.
func violations(root string, standard map[string]bool) ([]string, error) {
	var failures []string
	err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filename != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "testdata" || entry.Name() == "vendor" || entry.Name() == "third_party" || entry.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(filename, ".go") {
			return nil
		}
		testSource := strings.HasSuffix(filename, "_test.go")
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		owner := path.Dir(name)
		positions := token.NewFileSet()
		file, err := parser.ParseFile(positions, filename, nil, 0)
		if err != nil {
			return err
		}
		report := func(pos token.Pos, rule, detail string) {
			failures = append(failures, fmt.Sprintf("%s:%d: %s: %s", name, positions.Position(pos).Line, rule, detail))
		}
		core := within(owner, "internal/core")
		ownerFamily := family(owner, "internal/core")
		if !testSource && core && layers[ownerFamily] == 0 {
			report(file.Package, "unmapped", owner+" has no C-40 layer")
		}
		if !testSource && !within(owner, "internal/archtest") && (within(owner, "internal") || within(owner, "cmd") || within(owner, "kit")) {
			ast.Inspect(file, func(n ast.Node) bool {
				literal, ok := n.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				value, e := strconv.Unquote(literal.Value)
				if e != nil {
					return true
				}
				for _, forbidden := range []string{"/tools/crw-takeover", "github.com/thisisjun786/codex-relay-workflow/", "codex-session-relay/", ".crw/", ".codex/plugins/crw"} {
					if strings.Contains(value, forbidden) {
						report(literal.Pos(), "operator", "product must not resolve an external coordination runtime or state path")
						break
					}
				}
				return true
			})
		}
		osNames := make(map[string]bool)
		dotOS := false
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			product := within(owner, "cmd") || within(owner, "internal")
			if ((!testSource && !within(owner, "tools")) || product) && within(imported, rootModule+"/tools/crw-takeover") {
				report(spec.Pos(), "operator tool", "takeover must remain outside product dependencies: "+imported)
			}
			if testSource {
				continue
			}
			if !within(owner, "internal/addons") && !(name == "cmd/lina/import_hermes.go" && imported == rootModule+"/internal/addons/importer/hermes") && within(imported, rootModule+"/internal/addons/importer") {
				report(spec.Pos(), "importer", "memory importer add-ons must not be dependencies of Core or default packages: "+imported)
			}
			if within(imported, rootModule+"/internal/core") {
				target := family(imported, rootModule+"/internal/core")
				if layers[target] == 0 {
					report(spec.Pos(), "unmapped", imported+" has no C-40 layer")
				} else if core && ownerFamily != target && layers[target] >= layers[ownerFamily] {
					report(spec.Pos(), "layer", fmt.Sprintf("%s (L%d) imports %s (L%d); imports between families must run downward", ownerFamily, layers[ownerFamily], target, layers[target]))
				}
			}
			if within(owner, "kit") && within(imported, rootModule) && !within(imported, rootModule+"/kit") {
				report(spec.Pos(), "kit", "kit must not import the root module: "+imported)
			}
			moirai := within(owner, "internal/moirai") || within(owner, "cmd/moirai-worker")
			if moirai {
				if moiraiLayer(owner) == 0 {
					report(spec.Pos(), "Moirai", "unmapped worker package: "+owner)
				}
				if within(imported, rootModule+"/internal/moirai") {
					target := strings.TrimPrefix(imported, rootModule+"/")
					if moiraiLayer(target) == 0 || (owner != target && moiraiLayer(target) >= moiraiLayer(owner)) {
						report(spec.Pos(), "Moirai", "worker imports must run downward: "+imported)
					}
				}

				allowed := standard[imported] || within(imported, rootModule+"/internal/moirai")
				if owner == "internal/moirai/canon" && (imported == "github.com/ncruces/go-sqlite3" || imported == "github.com/ncruces/go-sqlite3/driver" || imported == "github.com/ncruces/go-sqlite3/embed") {
					allowed = true
				}
				if !allowed {
					report(spec.Pos(), "Moirai", "worker may import only its read-only reader, job API, runtime and standard library: "+imported)
				}
			}
			if core && within(imported, rootModule+"/internal/moirai") && imported != rootModule+"/internal/moirai/jobapi" {
				report(spec.Pos(), "Moirai", "Core may import only the worker's job API contract: "+imported)
			}
			if within(owner, "internal/buildinfo") && !standard[imported] {
				report(spec.Pos(), "buildinfo", "build metadata may import only the standard library: "+imported)
			}
			if within(owner, "internal/tui") {
				allowed := standard[imported] || within(imported, rootModule+"/internal/clientapi") ||
					within(imported, rootModule+"/internal/design") || within(imported, rootModule+"/internal/tui") ||
					(within(imported, rootModule+"/kit/protocol") || imported == rootModule+"/kit/secret")
				charm := within(imported, "github.com/charmbracelet") || within(imported, "charm.land")
				terminal := within(owner, "internal/tui/term") || within(owner, "internal/tui/ui")
				// Goldmark and Chroma supply semantic spans only at the UI adapter.
				// Keep the pure reducer/render/layout layers dependency-free.
				markdown := within(owner, "internal/tui/ui") && (imported == "github.com/yuin/goldmark" ||
					imported == "github.com/yuin/goldmark/ast" || imported == "github.com/yuin/goldmark/text" ||
					imported == "github.com/yuin/goldmark/util" || imported == "github.com/yuin/goldmark/parser" ||
					imported == "github.com/alecthomas/chroma/v2" || imported == "github.com/alecthomas/chroma/v2/lexers")
				if !allowed && !(charm && terminal) && !markdown {
					report(spec.Pos(), "TUI", "only standard/internal client dependencies and permitted UI/terminal libraries are allowed: "+imported)
				}
			}
			if (within(imported, "github.com/charmbracelet") || within(imported, "charm.land")) && !within(owner, "internal/tui/term") && !within(owner, "internal/tui/ui") {
				report(spec.Pos(), "Charm", "only internal/tui/term and internal/tui/ui may import "+imported)
			}
			if imported == "os/exec" && !within(owner, "kit/proc") && !within(owner, "kit/sandbox") {
				report(spec.Pos(), "os/exec", "process creation belongs in kit/proc or kit/sandbox")
			}
			if within(owner, "internal/node") || within(owner, "cmd/lina-node") {
				allowedKit := within(imported, rootModule+"/kit") && nodeKit[family(imported, rootModule+"/kit")]
				allowedBuildinfo := owner == "cmd/lina-node" && imported == rootModule+"/internal/buildinfo"
				if !standard[imported] && !allowedKit && !allowedBuildinfo {
					report(spec.Pos(), "Node", "only the standard library, C-35 kit packages and cmd/lina-node's buildinfo import are allowed: "+imported)
				}
			}
			if imported == "os" {
				alias := "os"
				if spec.Name != nil {
					alias = spec.Name.Name
				}
				dotOS = alias == "."
				osNames[alias] = alias != "_" && alias != "."
			}
		}
		if !within(owner, "kit/proc") {
			// Resolve import aliases and local shadowing, and catch taking the function as
			// a value as well as calling it. Selector fields are not dot-import references.
			selectors := make(map[*ast.Ident]bool)
			ast.Inspect(file, func(node ast.Node) bool {
				if selector, ok := node.(*ast.SelectorExpr); ok {
					selectors[selector.Sel] = true
				}
				return true
			})
			ast.Inspect(file, func(node ast.Node) bool {
				switch value := node.(type) {
				case *ast.SelectorExpr:
					if ident, ok := value.X.(*ast.Ident); ok && ident.Obj == nil && osNames[ident.Name] && value.Sel.Name == "Environ" {
						report(value.Pos(), "os.Environ", "environment capture belongs in kit/proc")
					}
				case *ast.Ident:
					if dotOS && value.Name == "Environ" && value.Obj == nil && !selectors[value] {
						report(value.Pos(), "os.Environ", "environment capture belongs in kit/proc")
					}
				}
				return true
			})
		}
		return nil
	})
	return failures, err
}
