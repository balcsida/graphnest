package webui

import (
	"bytes"
	"os/exec"
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
		`.repository-group>h2{margin:0;min-width:0;overflow-wrap:anywhere`,
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
		`const body={repository_id:match.repository.id,path:match.path}`,
		`if(match.start_line)body.start_line=match.start_line`,
		`if(response.indexed_sha!==match.sha)throw new Error("Indexed revision changed. Search again.")`,
		`gutter.textContent=String(response.start_line+offset)`,
		`$("file-lines").replaceChildren(fragment)`,
		`response.truncated?"File content was truncated.":""`,
		`href=blobURL(match)`,
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
		`b.addEventListener("click",()=>void openFile(`,
		`const href=indexedLocationURL(l)`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing exact SCIP navigation behavior %q", want)
		}
	}
}

func TestConsoleOpensSCIPLocationsInFileViewer(t *testing.T) {
	script, err := elementBody(string(document), "script")
	if err != nil {
		t.Fatal(err)
	}
	renderNavigation, err := functionBody(script, "renderNavigation")
	if err != nil {
		t.Fatal(err)
	}
	indexedLocationURL, err := functionBody(script, "indexedLocationURL")
	if err != nil {
		t.Fatal(err)
	}
	blobURL, err := functionBody(script, "blobURL")
	if err != nil {
		t.Fatal(err)
	}
	harness := `
class Node {
  constructor(tag){this.tag=tag;this.children=[];this.listeners={};this.dataset={}}
  append(...children){this.children.push(...children)}
  replaceChildren(...children){this.children=children}
  addEventListener(name,listener){this.listeners[name]=listener}
  click(){this.listeners.click()}
}
const root=new Node("root"),document={
  createDocumentFragment:()=>new Node("fragment"),
  createElement:tag=>new Node(tag)
},$=()=>root,opened=[],state={fileTrigger:{id:"search-result"}};
const openFile=match=>opened.push(match);
` + blobURL + indexedLocationURL + renderNavigation + `
const sha="0123456789abcdef0123456789abcdef01234567";
renderNavigation({locations:[{repository_id:202,repository_name:"acme/lib",web_url:"https://github.example/acme/lib",commit:sha,path:"lib.go",start_line:7,symbol:"sym"}]});
const nodes=root.children[0].children[0].children;
const button=nodes.find(node=>node.tag==="button"),link=nodes.find(node=>node.tag==="a");
if(!button)throw new Error("SCIP location has no in-app control");
button.click();
const match=opened[0];
if(match.repository.id!==202||match.repository.name!=="acme/lib"||match.repository.web_url!=="https://github.example/acme/lib"||match.sha!==sha||match.path!=="lib.go"||match.line_number!==7)throw new Error("SCIP location opened the wrong indexed file");
if(state.fileTrigger.id!=="search-result")throw new Error("search-result focus trigger was replaced");
if(!link||link.href!=="https://github.example/acme/lib/blob/"+sha+"/lib.go#L7")throw new Error("indexed-source fallback is missing");
renderNavigation({locations:[{repository_id:303,repository_name:"acme/cross",web_url:"",commit:sha,path:"cross.go",start_line:9}]});
const crossNodes=root.children[0].children[0].children;
if(!crossNodes.find(node=>node.tag==="button"))throw new Error("cross-repository location has no in-app control");
if(crossNodes.some(node=>node.tag==="a"))throw new Error("cross-repository location invented an external URL");
`
	if output, err := exec.Command(requireNode(t), "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("SCIP location behavior failed: %v\n%s", err, output)
	}
}

func TestConsoleFocusesOpenedSCIPLine(t *testing.T) {
	script, err := elementBody(string(document), "script")
	if err != nil {
		t.Fatal(err)
	}
	renderFile, err := functionBody(script, "renderFile")
	if err != nil {
		t.Fatal(err)
	}
	harness := `
class Node {
  constructor(){this.children=[];this.dataset={};this.classList={add(){},remove(){}}}
  append(...children){this.children.push(...children)}
  replaceChildren(...children){this.children=children}
  addEventListener(){}
  focus(){this.focused=true}
}
const root=new Node(),document={
  createDocumentFragment:()=>new Node(),
  createElement:()=>new Node(),
  createTextNode:text=>text
},$=()=>root,state={fileMatch:{line_number:7}};
const selectIdentifier=()=>{};
` + renderFile + `
renderFile({content:"target",start_line:7});
const row=root.children[0].children[0];
if(!row.focused||row.tabIndex!==-1)throw new Error("opened SCIP line was not focused");
`
	if output, err := exec.Command(requireNode(t), "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("SCIP target-line focus failed: %v\n%s", err, output)
	}
}

func TestConsoleRequestsTheOpenedSCIPLine(t *testing.T) {
	script, err := elementBody(string(document), "script")
	if err != nil {
		t.Fatal(err)
	}
	openFile, err := functionBody(script, "openFile")
	if err != nil {
		t.Fatal(err)
	}
	openFile = "async " + openFile
	blobURL, err := functionBody(script, "blobURL")
	if err != nil {
		t.Fatal(err)
	}
	renderNavigation, err := functionBody(script, "renderNavigation")
	if err != nil {
		t.Fatal(err)
	}
	indexedLocationURL, err := functionBody(script, "indexedLocationURL")
	if err != nil {
		t.Fatal(err)
	}
	harness := `
class Node {
  constructor(tag){this.tag=tag;this.children=[];this.listeners={};this.dataset={};this.hidden=false;this.textContent=""}
  append(...children){this.children.push(...children)}
  replaceChildren(...children){this.children=children}
  addEventListener(name,listener){this.listeners[name]=listener}
  click(){return this.listeners.click()}
  removeAttribute(){}
  focus(){}
}
const elements=new Map(),$=id=>{
  if(!elements.has(id))elements.set(id,new Node(id));
  return elements.get(id)
},state={fileController:null,navigationController:null,navigationGeneration:0,selectedIdentifier:null,navigationRetry:null,fileGeneration:0,fileMatch:null,fileRetry:null},requests=[];
const document={createDocumentFragment:()=>new Node("fragment"),createElement:tag=>new Node(tag)};
const request=async(path,init)=>{const body=JSON.parse(init.body);requests.push({path,body});return {indexed_sha:sha,content:"target",start_line:body.start_line||1,truncated:false}};
const showScreen=()=>{},renderFile=response=>{rendered=response},showStatusError=()=>{};
let rendered;
` + blobURL + indexedLocationURL + openFile + renderNavigation + `
const sha="0123456789abcdef0123456789abcdef01234567",repository={id:202,name:"acme/lib",web_url:""};
await openFile({repository,sha,path:"lib.go",line_number:12});
if("start_line" in requests[0].body)throw new Error("ordinary search result changed its file window");
renderNavigation({locations:[{repository_id:202,repository_name:"acme/lib",web_url:"",commit:sha,path:"lib.go",start_line:1500}]});
const button=$("navigation-locations").children[0].children[0].children.find(node=>node.tag==="button");
button.click();
await new Promise(resolve=>setImmediate(resolve));
if(requests[1].path!=="/v1/files/read"||requests[1].body.start_line!==1500)throw new Error("SCIP target line was not requested");
if(rendered.start_line!==1500)throw new Error("SCIP file response was not rendered");
`
	if output, err := exec.Command(requireNode(t), "--input-type=module", "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("SCIP file request failed: %v\n%s", err, output)
	}
}

func TestConsoleOmitsInvalidSearchResultLink(t *testing.T) {
	script, err := elementBody(string(document), "script")
	if err != nil {
		t.Fatal(err)
	}
	renderResults, err := functionBody(script, "renderResults")
	if err != nil {
		t.Fatal(err)
	}
	groupMatches, err := functionBody(script, "groupMatches")
	if err != nil {
		t.Fatal(err)
	}
	blobURL, err := functionBody(script, "blobURL")
	if err != nil {
		t.Fatal(err)
	}
	harness := `
class Node {
  constructor(tag){this.tag=tag;this.children=[];this.dataset={}}
  append(...children){this.children.push(...children)}
  replaceChildren(...children){this.children=children}
  addEventListener(){}
}
const roots=new Map(),$=id=>{if(!roots.has(id))roots.set(id,new Node(id));return roots.get(id)},document={
  createDocumentFragment:()=>new Node("fragment"),
  createElement:tag=>new Node(tag)
},state={},openFile=()=>{},countLabel=(n,s)=>n+" "+s;
const links=node=>[...(node.tag==="a"?[node]:[]),...node.children.flatMap(links)];
` + blobURL + groupMatches + renderResults + `
const sha="0123456789abcdef0123456789abcdef01234567",base={repository:{id:1,name:"acme/one",web_url:""},sha,path:"main.go",line_number:1,preview:"x"};
renderResults({matches:[base]});
if(links($("results")).length)throw new Error("invalid search result invented an external URL");
renderResults({matches:[{...base,repository:{...base.repository,web_url:"https://github.example/acme/one"}}]});
const valid=links($("results"));
if(valid.length!==1||valid[0].href!=="https://github.example/acme/one/blob/"+sha+"/main.go#L1")throw new Error("valid search-result link is missing");
`
	if output, err := exec.Command(requireNode(t), "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("search-result link behavior failed: %v\n%s", err, output)
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
