package webui

import (
	"bytes"
	"testing"
)

func TestConsoleKeepsBrowserPerformanceBudget(t *testing.T) {
	if len(document) >= 40<<10 {
		t.Fatalf("document bytes=%d, want less than %d", len(document), 40<<10)
	}
	if got := bytes.Count(document, []byte("<style>")); got != 1 {
		t.Fatalf("style blocks=%d, want 1", got)
	}
	if got := bytes.Count(document, []byte("<script>")); got != 1 {
		t.Fatalf("script blocks=%d, want 1", got)
	}
	for _, forbidden := range []string{
		`<script src=`, `<link rel="stylesheet"`, `@import`, `sourceMappingURL`,
		`setInterval(`, `setTimeout(`, `requestAnimationFrame(`,
		`addEventListener("scroll"`, `addEventListener("resize"`,
		`animation:reveal`,
	} {
		if bytes.Contains(document, []byte(forbidden)) {
			t.Fatalf("console exceeds steady-state budget with %q", forbidden)
		}
	}
	for _, want := range []string{
		`document.createDocumentFragment()`,
		`groupMatches(response.matches)`,
		`$("results").replaceChildren(fragment)`,
		`state.controller.abort()`,
		`code.textContent=text`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing bounded rendering behavior %q", want)
		}
	}
}
