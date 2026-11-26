package ops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestBackendSupportMatrixCoversCodecOptions keeps docs/IMGPARITY.md from
// drifting: every field on a codec option struct must be named in the backend
// support matrix. Adding a field without documenting it fails here.
func TestBackendSupportMatrixCoversCodecOptions(t *testing.T) {
	src, err := os.ReadFile("codec.go")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile("../../../docs/IMGPARITY.md")
	if err != nil {
		t.Fatal(err)
	}
	const heading = "## Backend support matrix"
	idx := strings.Index(string(doc), heading)
	if idx < 0 {
		t.Fatalf("docs/IMGPARITY.md has no %q section", heading)
	}
	section := string(doc[idx:])

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "codec.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	prefixes := map[string]string{
		"PNGEncoder":  "pngo",
		"WebPEncoder": "webpo",
		"AVIFEncoder": "avifo",
	}
	// Field names do not always snake-case to the URL argument name.
	names := map[string]map[string]string{
		"PNGEncoder": {
			"Interlaced": "pngo_interlaced",
			"Quantize":   "pngo_quantize",
			"Colors":     "pngo_quantization_colors",
		},
		"WebPEncoder": {
			"Compression":    "webpo_compression",
			"SmartSubsample": "webpo_smart_subsample",
			"Preset":         "webpo_preset",
		},
		"AVIFEncoder": {
			"Subsample": "avifo_subsample",
		},
	}

	found := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok {
			return true
		}
		// Find the type name via the enclosing declaration.
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Type != st {
					continue
				}
				prefix, ok := prefixes[ts.Name.Name]
				if !ok {
					continue
				}
				for _, field := range st.Fields.List {
					for _, id := range field.Names {
						want, ok := names[ts.Name.Name][id.Name]
						if !ok {
							t.Errorf("%s.%s has no documented matrix name (prefix %s)", ts.Name.Name, id.Name, prefix)
							continue
						}
						found[want] = true
					}
				}
			}
		}
		return true
	})

	if len(found) == 0 {
		t.Fatal("parsed no codec option fields from codec.go")
	}
	for name := range found {
		if !strings.Contains(section, "`"+name+"`") {
			t.Errorf("backend support matrix does not mention %s", name)
		}
	}
}
