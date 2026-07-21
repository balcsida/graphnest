package webui

import (
	"bytes"
	"testing"
)

func TestConsoleRendersGroupedCodeResults(t *testing.T) {
	for _, want := range []string{
		`group.className="repository-group"`,
		`file.className="file-result"`,
		`header.className="file-header"`,
		`block.className="match-block"`,
		`row.className="code-row"`,
		`gutter.className="line-gutter"`,
		`viewport.className="code-viewport"`,
		`preview.replace(/\n$/,"").split("\n")`,
		`gutter.textContent=String(match.line_number+offset)`,
		`link.textContent="Open indexed source"`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing grouped result rendering %q", want)
		}
	}
	for _, forbidden := range []string{
		`card.className="result"`,
		`animation:reveal`,
	} {
		if bytes.Contains(document, []byte(forbidden)) {
			t.Fatalf("console still contains obsolete result behavior %q", forbidden)
		}
	}
}

func TestConsoleBoundsLongRepositoryLabels(t *testing.T) {
	for _, want := range []string{
		`width:min(300px,calc(100vw - 40px))`,
		`fieldset label{display:flex;gap:8px;align-items:center;min-width:0;min-height:44px;overflow-wrap:anywhere}`,
		`.repository-group>h2{min-width:0;overflow-wrap:anywhere}`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing long repository-label protection %q", want)
		}
	}
	if bytes.Contains(document, []byte(`max-width:100%;margin-top:4px`)) {
		t.Fatal("repository popup is capped by its narrow details parent")
	}
}
