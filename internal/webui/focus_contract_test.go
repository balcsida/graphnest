package webui

import (
	"bytes"
	"testing"
)

func TestConsoleUsesAccentForSharedFocusOutline(t *testing.T) {
	for _, want := range []string{
		`:focus-visible{outline:3px solid var(--accent);outline-offset:3px}`,
		`.match-block{border-top:1px solid var(--border);border-left:4px solid var(--match)}`,
		`@media(forced-colors: active){button,input,summary,fieldset,.file-result,.repository-table,.match-block{border:1px solid CanvasText}.match-block{border-left-width:4px}}`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing grouped result style %q", want)
		}
	}
	for _, obsolete := range []string{`.result{`, `.result a{`} {
		if bytes.Contains(document, []byte(obsolete)) {
			t.Fatalf("console retains obsolete result style %q", obsolete)
		}
	}
}

func TestConsoleMovesFocusIntoAndBackFromFileViewer(t *testing.T) {
	for _, want := range []string{
		`fileTrigger:null`,
		`state.fileTrigger=null`,
		`state.fileTrigger=pathButton;void openFile(matches[0])`,
		`$("file-back").focus()`,
		`function closeFile(){const trigger=state.fileTrigger;showScreen("search");(trigger&&trigger.isConnected?trigger:$("query")).focus()}`,
		`$("file-back").addEventListener("click",closeFile)`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing file-view focus behavior %q", want)
		}
	}
}
