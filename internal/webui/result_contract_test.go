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
