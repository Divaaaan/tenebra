package state

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCanonicalImportBoundaries(t *testing.T) {
	for _, boundary := range []struct {
		directory string
		forbidden []string
	}{
		{".", []string{"github.com/Divaaaan/tenebra/core/control", "github.com/Divaaaan/tenebra/internal/installtx"}},
		{"../profile", []string{"github.com/Divaaaan/tenebra/core/state"}},
	} {
		entries, err := os.ReadDir(boundary.directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			name := filepath.Join(boundary.directory, entry.Name())
			file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imported := range file.Imports {
				path, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				for _, forbidden := range boundary.forbidden {
					if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
						t.Errorf("%s imports forbidden authority %s", name, path)
					}
				}
			}
		}
	}
}
