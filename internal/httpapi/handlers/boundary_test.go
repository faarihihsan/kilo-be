package handlers

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestHandlersDoNotImportStore enforces the layering rule of the package doc:
// handlers go through the service layer, never straight to SQL.
func TestHandlersDoNotImportStore(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files found: %v", err)
	}
	forbidden := []string{
		"workout-tracker-be/internal/store",
		"github.com/jackc/pgx",
		"database/sql",
	}
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, bad := range forbidden {
				if path == bad || strings.HasPrefix(path, bad+"/") {
					t.Errorf("%s imports %q: handlers must go through the service layer", name, path)
				}
			}
		}
	}
}
