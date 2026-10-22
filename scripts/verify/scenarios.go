// SPDX-License-Identifier: Apache-2.0
// Read Go declarations with the same parser used by the toolchain. This helper
// does not execute scenario code or compile a test binary during selection.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type request struct {
	Root     string
	Packages []struct {
		Path  string
		Files []string
	}
}

type declaration struct {
	Name          string
	Global        bool
	References    []string
	Strings       []string
	Binaries      []string
	UnknownBinary bool
}

type source struct {
	Path         string
	Package      string
	Name         string
	Imports      []string
	Tests        []string
	Declarations []declaration
}

func sorted(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func literal(expr ast.Expr) string {
	if value, ok := expr.(*ast.BasicLit); ok && value.Kind == token.STRING {
		text, _ := strconv.Unquote(value.Value)
		return text
	}
	return ""
}

func describe(name string, node ast.Node, global bool) declaration {
	refs, texts, binaries := map[string]bool{}, map[string]bool{}, map[string]bool{}
	unknownBinary := false
	ast.Inspect(node, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.Ident:
			refs[value.Name] = true
		case *ast.BasicLit:
			if value.Kind == token.STRING {
				text, _ := strconv.Unquote(value.Value)
				texts[text] = true
				for _, prefix := range []string{"./cmd/", "./tools/"} {
					if strings.HasPrefix(text, prefix) && len(text) > len(prefix) && !strings.ContainsAny(text, " \n\t") {
						binaries[strings.TrimPrefix(text, "./")] = true
					}
				}
			}
		case *ast.CallExpr:
			if function, ok := value.Fun.(*ast.Ident); ok && function.Name == "buildCommand" && len(value.Args) > 1 {
				if command := literal(value.Args[1]); command != "" {
					binaries["cmd/"+command] = true
				} else {
					unknownBinary = true
				}
			}
			if function, ok := value.Fun.(*ast.SelectorExpr); ok && (function.Sel.Name == "Command" || function.Sel.Name == "CommandContext") && name != "buildCommand" {
				build, target := false, false
				for _, arg := range value.Args {
					text := literal(arg)
					build = build || text == "build"
					target = target || strings.HasPrefix(text, "./cmd/") || strings.HasPrefix(text, "./tools/")
				}
				unknownBinary = unknownBinary || (build && !target)
			}
		}
		return true
	})
	return declaration{Name: name, Global: global, References: sorted(refs), Strings: sorted(texts), Binaries: sorted(binaries), UnknownBinary: unknownBinary}
}

func runnable(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) {
		return true
	}
	next, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(next)
}

func scan(root, name, pkg string) (source, error) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return source{}, fmt.Errorf("scenario source escapes repository: %s", name)
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, clean), nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return source{}, err
	}
	result := source{Path: filepath.ToSlash(clean), Package: pkg, Name: file.Name.Name}
	for _, imported := range file.Imports {
		value, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			return source{}, err
		}
		result.Imports = append(result.Imports, value)
	}
	tests := map[string]bool{}
	for _, node := range file.Decls {
		switch value := node.(type) {
		case *ast.FuncDecl:
			name := value.Name.Name
			result.Declarations = append(result.Declarations, describe(name, value, value.Recv == nil && (name == "init" || name == "TestMain")))
			if value.Recv == nil && name != "TestMain" && (runnable(name, "Test") || runnable(name, "Fuzz")) {
				tests[name] = true
			}
		case *ast.GenDecl:
			for _, spec := range value.Specs {
				switch spec := spec.(type) {
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						result.Declarations = append(result.Declarations, describe(name.Name, spec, value.Tok == token.VAR))
					}
				case *ast.TypeSpec:
					result.Declarations = append(result.Declarations, describe(spec.Name.Name, spec, false))
				}
			}
		}
	}
	for _, example := range doc.Examples(file) {
		if example.Output != "" || example.EmptyOutput {
			tests["Example"+example.Name] = true
		}
	}
	if !strings.HasSuffix(clean, "_test.go") {
		tests = map[string]bool{}
	}
	result.Tests = sorted(tests)
	return result, nil
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: scenarios request.json")
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		return err
	}
	var input request
	if err := json.Unmarshal(data, &input); err != nil {
		return err
	}
	result := []source{}
	for _, pkg := range input.Packages {
		for _, file := range pkg.Files {
			record, err := scan(input.Root, file, pkg.Path)
			if err != nil {
				return err
			}
			result = append(result, record)
		}
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
