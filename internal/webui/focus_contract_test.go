package webui

import (
	"bytes"
	"testing"
)

func TestConsoleUsesSignalBlueForSharedFocusOutline(t *testing.T) {
	if !bytes.Contains(document, []byte(`:focus-visible{outline:3px solid var(--signal);outline-offset:3px}`)) {
		t.Fatal("shared focus outline does not use Signal blue")
	}
	if !bytes.Contains(document, []byte(`border-left:4px solid var(--match)`)) {
		t.Fatal("result emphasis no longer uses Match amber")
	}
}
