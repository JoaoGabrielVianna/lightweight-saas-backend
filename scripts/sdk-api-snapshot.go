//go:build ignore

// sdk-api-snapshot.go — print the SDK's exported API as a deterministic,
// reviewable text file.
//
// # Why this exists
//
// Until Slice 16 nothing in this repository could tell the difference between
// "the SDK changed" and "the SDK's PROMISE changed". Renaming an exported
// method, dropping a field, or adding a parameter all look identical to `go
// test`: the SDK's own tests are updated in the same commit, so they pass, and
// the breakage lands on consumers who were not in the room.
//
// A snapshot turns that into a diff. Nothing here PREVENTS a breaking change —
// pre-v1 the SDK is allowed to make them — it only makes one impossible to make
// by accident, and impossible to ship without someone having typed
// `make sdk-api-update` and looked at what they were promising.
//
// # Why not apidiff
//
// golang.org/x/exp/apidiff classifies changes as compatible or breaking, which
// is more than this needs and costs a tool dependency plus a module graph to
// keep pinned. A sorted text file gives the reviewer the same information in a
// git diff they were already going to read.
//
// # Why go/ast and not `go doc`
//
// `go doc -all` renders prose, wraps at a terminal width, and reorders as
// documentation rather than as data — its output moves for reasons that have
// nothing to do with the API. Parsing declarations and printing normalized
// signatures gives a file whose every line change means an actual API change.
//
// # Why it is `//go:build ignore`
//
// It belongs to the repository, not to the server binary and not to the SDK
// module. Under this constraint `go list ./...` does not see it, so it cannot
// move the package count or the coverage denominator, while gofmt still checks
// its formatting. Run it by naming the file:
//
//	go run scripts/sdk-api-snapshot.go sdk/go
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/sdk-api-snapshot.go <sdk dir>")
		os.Exit(2)
	}
	out, err := snapshot(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "sdk-api-snapshot:", err)
		os.Exit(1)
	}
	fmt.Print(out)
}

func snapshot(dir string) (string, error) {
	fset := token.NewFileSet()

	// Test files are excluded, and so is every file the build would exclude.
	// A snapshot that moved when a _test.go file did would be a snapshot of the
	// wrong thing: consumers cannot see test files.
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return "", err
	}

	var modulePath string
	if mod, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		for _, line := range strings.Split(string(mod), "\n") {
			if strings.HasPrefix(line, "module ") {
				modulePath = strings.TrimSpace(strings.TrimPrefix(line, "module "))
				break
			}
		}
	}

	var lines []string
	for name, pkg := range pkgs {
		if name == "main" {
			continue // cmd/example is a program, not a promise
		}
		for _, file := range pkg.Files {
			lines = append(lines, declarations(fset, file)...)
		}
	}

	// Sorted, so the file's order is a property of the API rather than of the
	// filesystem walk. Two runs on two machines must produce byte-identical
	// output or the gate below is a coin toss.
	sort.Strings(lines)
	lines = dedupe(lines)

	var b strings.Builder
	b.WriteString("# Exported API of the LIGHTWEIGHT Go SDK.\n")
	b.WriteString("#\n")
	b.WriteString("# GENERATED — do not edit. Regenerate with: make sdk-api-update\n")
	b.WriteString("#\n")
	b.WriteString("# Every line here is a promise to someone who has already shipped. A diff in\n")
	b.WriteString("# this file is the review asking whether that promise is being broken on\n")
	b.WriteString("# purpose. Pre-v1 it may be; silently is the only forbidden way.\n")
	b.WriteString("#\n")
	fmt.Fprintf(&b, "# module %s\n\n", modulePath)
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String(), nil
}

func dedupe(in []string) []string {
	out := in[:0]
	var prev string
	for i, s := range in {
		if i > 0 && s == prev {
			continue
		}
		out = append(out, s)
		prev = s
	}
	return out
}

// declarations renders every exported declaration in one file.
func declarations(fset *token.FileSet, file *ast.File) []string {
	var out []string

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() {
				continue
			}
			if d.Recv == nil {
				out = append(out, "func "+d.Name.Name+render(fset, funcSig(d.Type)))
				continue
			}
			// A method on an unexported type is not reachable by a consumer
			// except through something exported that returns it; rendering it
			// under its real receiver keeps the file honest either way.
			recv := render(fset, d.Recv.List[0].Type)
			if !exportedTypeName(recv) {
				continue
			}
			out = append(out, "method ("+recv+") "+d.Name.Name+render(fset, funcSig(d.Type)))

		case *ast.GenDecl:
			out = append(out, genDecl(fset, d)...)
		}
	}
	return out
}

func genDecl(fset *token.FileSet, d *ast.GenDecl) []string {
	var out []string
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if !s.Name.IsExported() {
				continue
			}
			out = append(out, typeSpec(fset, s)...)

		case *ast.ValueSpec:
			kw := "var"
			if d.Tok == token.CONST {
				kw = "const"
			}
			for _, name := range s.Names {
				if !name.IsExported() {
					continue
				}
				line := kw + " " + name.Name
				// The TYPE is part of the promise; the VALUE mostly is not.
				// An exception that matters: an exported string constant IS its
				// value to a consumer comparing against it, so those are kept.
				if s.Type != nil {
					line += " " + render(fset, s.Type)
				}
				if lit := stringLiteral(s, name); lit != "" {
					line += " = " + lit
				}
				out = append(out, line)
			}
		}
	}
	return out
}

// stringLiteral returns the source text of name's initializer when it is a
// plain string constant, and "" otherwise.
//
// These are the SDK's stable machine-readable error codes and scope names. A
// consumer writes `err.Code == lightweight.CodeInsufficientScope`, but what
// actually crosses the wire is the STRING — so changing the value while keeping
// the identifier breaks every such comparison without touching a signature.
// That change would be invisible in a snapshot that recorded only types.
func stringLiteral(s *ast.ValueSpec, name *ast.Ident) string {
	for i, n := range s.Names {
		if n != name || i >= len(s.Values) {
			continue
		}
		if lit, ok := s.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			return lit.Value
		}
	}
	return ""
}

func typeSpec(fset *token.FileSet, s *ast.TypeSpec) []string {
	name := s.Name.Name

	switch t := s.Type.(type) {
	case *ast.StructType:
		out := []string{"type " + name + " struct"}
		for _, f := range t.Fields.List {
			ft := render(fset, f.Type)
			if len(f.Names) == 0 {
				out = append(out, "field "+name+" embeds "+ft) // embedded
				continue
			}
			for _, fn := range f.Names {
				if !fn.IsExported() {
					continue
				}
				out = append(out, "field "+name+"."+fn.Name+" "+ft)
			}
		}
		return out

	case *ast.InterfaceType:
		out := []string{"type " + name + " interface"}
		for _, m := range t.Methods.List {
			for _, mn := range m.Names {
				if !mn.IsExported() {
					continue
				}
				if ft, ok := m.Type.(*ast.FuncType); ok {
					out = append(out, "method "+name+"."+mn.Name+render(fset, funcSig(ft)))
				}
			}
		}
		return out

	default:
		// Named types over a basic or composite type: `type Code string`.
		return []string{"type " + name + " " + render(fset, s.Type)}
	}
}

// funcSig strips the leading `func` keyword's position but keeps params and
// results, so a signature renders as `(ctx context.Context) (*UserPage, error)`.
func funcSig(t *ast.FuncType) ast.Expr {
	cp := *t
	cp.Func = token.NoPos
	return &cp
}

func render(fset *token.FileSet, e ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, e); err != nil {
		return "<unprintable>"
	}
	s := buf.String()
	// printer emits `func(a) b` for a *ast.FuncType; the caller supplies its own
	// keyword, so drop the duplicate.
	return strings.TrimPrefix(s, "func")
}

// exportedTypeName reports whether a rendered receiver names an exported type,
// looking past the pointer star and any type parameters.
func exportedTypeName(recv string) bool {
	recv = strings.TrimPrefix(recv, "*")
	if i := strings.IndexByte(recv, '['); i >= 0 {
		recv = recv[:i]
	}
	if recv == "" {
		return false
	}
	c := recv[0]
	return c >= 'A' && c <= 'Z'
}
