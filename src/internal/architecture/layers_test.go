package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The dependency rules are architectural contracts, not a list of today's
// files. A new adapter/native file is checked automatically.
func TestLayerDependencies(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		relative = filepath.ToSlash(relative)
		layer := ""
		switch {
		case strings.HasPrefix(relative, "domains/"):
			layer = "domain"
		case strings.HasPrefix(relative, "usecase/"):
			layer = "application"
		case strings.HasPrefix(relative, "ui/rest/"):
			layer = "http"
		case strings.HasPrefix(relative, "infrastructure/storage/"):
			layer = "storage"
		case strings.HasPrefix(relative, "internal/balemeow/"), strings.HasPrefix(relative, "internal/eitaameow/"), strings.HasPrefix(relative, "internal/rubikameow/"):
			layer = "native"
		default:
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range parsed.Imports {
			dependency, _ := strconv.Unquote(spec.Path.Value)
			local := strings.TrimPrefix(dependency, "github.com/mimalef70/goomni/src/")
			forbidden := false
			switch layer {
			case "domain":
				forbidden = strings.HasPrefix(local, "infrastructure/") || strings.HasPrefix(local, "internal/") || strings.HasPrefix(local, "ui/") || strings.HasPrefix(local, "usecase/") || dependency == "database/sql"
			case "application":
				forbidden = strings.HasPrefix(local, "infrastructure/storage") || strings.HasPrefix(local, "infrastructure/providers") || strings.HasPrefix(local, "ui/") || strings.HasPrefix(local, "internal/") || dependency == "database/sql"
			case "http":
				forbidden = strings.HasPrefix(local, "infrastructure/storage") || strings.HasPrefix(local, "infrastructure/providers") || strings.HasPrefix(local, "internal/") || dependency == "database/sql"
			case "storage":
				forbidden = strings.HasPrefix(local, "infrastructure/providers") || strings.HasPrefix(local, "internal/") || strings.HasPrefix(local, "ui/") || strings.HasPrefix(local, "usecase/")
			case "native":
				forbidden = strings.HasPrefix(local, "infrastructure/") || strings.HasPrefix(local, "ui/") || strings.HasPrefix(local, "usecase/") || strings.Contains(dependency, "gofiber") || dependency == "database/sql" || strings.Contains(dependency, "sqlite")
			}
			if forbidden {
				t.Errorf("%s layer %s imports %s", layer, relative, dependency)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
