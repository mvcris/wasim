package doccheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGodoc checks that every package has a package comment and that every
// exported identifier has a doc comment starting with its name, the
// convention pkg.go.dev and `go doc` rely on.
func TestGodoc(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if name := entry.Name(); path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata" || name == "e2e") {
			return filepath.SkipDir
		}
		checkDirectory(t, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func checkDirectory(t *testing.T, dir string) {
	t.Helper()
	files := token.NewFileSet()
	packages, err := parser.ParseDir(files, dir, func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range packages {
		documented := false
		for _, file := range pkg.Files {
			documented = documented || file.Doc != nil
			for _, decl := range file.Decls {
				checkDeclaration(t, files, decl)
			}
		}
		if !documented {
			t.Errorf("%s: package %s has no package comment", dir, pkg.Name)
		}
	}
}

func checkDeclaration(t *testing.T, files *token.FileSet, decl ast.Decl) {
	t.Helper()
	switch declaration := decl.(type) {
	case *ast.FuncDecl:
		if declaration.Name.IsExported() && exportedReceiver(declaration) {
			checkComment(t, files, declaration.Pos(), declaration.Name.Name, declaration.Doc)
		}
	case *ast.GenDecl:
		for _, spec := range declaration.Specs {
			switch value := spec.(type) {
			case *ast.TypeSpec:
				if value.Name.IsExported() {
					doc := value.Doc
					if doc == nil {
						doc = declaration.Doc
					}
					checkComment(t, files, value.Pos(), value.Name.Name, doc)
				}
			case *ast.ValueSpec:
				// A documented group covers its members, as in the standard library.
				if value.Doc != nil || declaration.Doc != nil || value.Comment != nil {
					continue
				}
				for _, name := range value.Names {
					if name.IsExported() {
						t.Errorf("%s: %s has no doc comment, nor does its group", files.Position(name.Pos()), name.Name)
					}
				}
			}
		}
	}
}

func exportedReceiver(function *ast.FuncDecl) bool {
	if function.Recv == nil {
		return true
	}
	receiver := function.Recv.List[0].Type
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		receiver = pointer.X
	}
	if generic, ok := receiver.(*ast.IndexExpr); ok {
		receiver = generic.X
	}
	identifier, ok := receiver.(*ast.Ident)
	return !ok || identifier.IsExported()
}

func checkComment(t *testing.T, files *token.FileSet, position token.Pos, name string, doc *ast.CommentGroup) {
	t.Helper()
	if doc == nil {
		t.Errorf("%s: %s has no doc comment", files.Position(position), name)
		return
	}
	text := doc.Text()
	for _, prefix := range []string{name + " ", "A " + name + " ", "An " + name + " ", "The " + name + " "} {
		if strings.HasPrefix(text, prefix) {
			return
		}
	}
	t.Errorf("%s: the doc comment of %s should start with its name: %q", files.Position(position), name, strings.SplitN(text, "\n", 2)[0])
}
