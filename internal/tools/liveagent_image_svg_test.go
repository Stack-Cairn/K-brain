package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Well-formed SVG documents display through Image and Read whatever their
// prologue: a license comment, a DOCTYPE (with an internal subset), a comment
// containing '>', or an xml-stylesheet processing instruction.
func TestLiveAgentImageWellFormedSVGPrologues(t *testing.T) {
	work := t.TempDir()
	ctx := WithWorkingDir(context.Background(), work)
	ts := LiveAgentCatalog()
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>`
	decl := `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
	doctype := `<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">` + "\n"
	for name, doc := range map[string]string{
		"comment without declaration": "<!-- Copyright 2026 Example Icons, MIT -->\n" + svg,
		"doctype without declaration": doctype + svg,
		"doctype internal subset":     decl + `<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd" [` + "\n" + `<!ENTITY ns_svg "http://www.w3.org/2000/svg">` + "\n]>\n" + svg,
		"comment containing gt":       decl + "<!-- exported: layers -> paths -->\n" + svg,
		"xml-stylesheet instruction":  decl + `<?xml-stylesheet type="text/css" href="icons.css"?>` + "\n" + svg,
	} {
		if err := os.WriteFile(filepath.Join(work, "icon.svg"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"path": "icon.svg"})
		for _, tool := range []string{"Image", "Read"} {
			if result := ExecuteResult(ctx, ts, tool, raw, true); result.Failed || len(result.Parts) != 1 {
				t.Errorf("%s via %s: failed=%v parts=%d text=%q", name, tool, result.Failed, len(result.Parts), result.Text)
			}
		}
	}
}
