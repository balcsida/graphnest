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

func TestConsoleOpensIndexedFilesInAFileViewer(t *testing.T) {
	for _, want := range []string{
		`id="file-view"`,
		`id="file-lines"`,
		`id="navigation-panel"`,
		`function openFile(match)`,
		`request("/v1/files/read"`,
		`body:JSON.stringify({repository_id:match.repository.id,path:match.path})`,
		`if(response.indexed_sha!==match.sha)throw new Error("Indexed revision changed. Search again.")`,
		`gutter.textContent=String(response.start_line+offset)`,
		`$("file-lines").replaceChildren(fragment)`,
		`response.truncated?"File content was truncated.":""`,
		`fallback.href=blobURL(match)`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing indexed file behavior %q", want)
		}
	}
}

func TestConsoleNavigatesIdentifiersAtExactOffsets(t *testing.T) {
	for _, want := range []string{
		`/[\p{L}_$][\p{L}\p{N}_$]*/gu`,
		`character_utf8:new TextEncoder().encode(prefix).length`,
		`character_utf16:prefix.length`,
		`character_utf32:Array.from(prefix).length`,
		`button.dataset.line=String(line)`,
		`function selectIdentifier(button)`,
		`function runNavigation(operation)`,
		`request("/v1/scip/navigation"`,
		`commit:match.sha`,
		`data-operation="definitions"`,
		`data-operation="references"`,
		`data-operation="implementations"`,
		`location.approximate?"Approximate":""`,
		`link.href=indexedLocationURL(location)`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing exact SCIP navigation behavior %q", want)
		}
	}
}

func TestConsoleUsesCountLabels(t *testing.T) {
	for _, want := range []string{
		`countLabel(response.matches.length,"match")`,
		`countLabel(repositories.size,"repository")`,
		`countLabel(groups.size,"repository")`,
		`countLabel(selected.length,"repository")`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing singular count grammar %q", want)
		}
	}
}
