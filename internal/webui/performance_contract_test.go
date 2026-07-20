package webui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestConsoleKeepsBrowserPerformanceBudget(t *testing.T) {
	if err := performanceBudgetError(document); err != nil {
		t.Fatal(err)
	}
}

func TestPerformanceBudgetRejectsResourceVariants(t *testing.T) {
	for _, test := range []struct {
		name     string
		resource string
	}{
		{"script attributes", `<SCRIPT defer SRC = 'app.js'></SCRIPT>`},
		{"stylesheet attributes", `<LiNk href='app.css' REL = "StyleSheet">`},
		{"font preload", `<link href="font.woff2" as = 'FONT' rel='preload'>`},
		{"image tag", `<IMG alt='' SRC = 'hero.png'>`},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := strings.Replace(validPerformanceDocument, "</head>", test.resource+"</head>", 1)
			if err := performanceBudgetError([]byte(doc)); err == nil {
				t.Fatalf("resource variant was accepted: %s", test.resource)
			}
		})
	}
}

func TestPerformanceBudgetRejectsSteadyStateVariants(t *testing.T) {
	for _, test := range []struct {
		name     string
		behavior string
		inStyles bool
	}{
		{"timer case", `SETTIMEOUT (() => {}, 0)`, false},
		{"timer whitespace", `setInterval (() => {}, 100)`, false},
		{"animation frame whitespace", `requestAnimationFrame (() => {})`, false},
		{"scroll listener quotes", `addEventListener ( 'scroll', onScroll)`, false},
		{"resize listener case", `addEventListener("RESIZE", onResize)`, false},
		{"result animation", `#results { ANIMATION : fade 1s; }`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			marker := "</script>"
			if test.inStyles {
				marker = "</style>"
			}
			doc := strings.Replace(validPerformanceDocument, marker, test.behavior+marker, 1)
			if err := performanceBudgetError([]byte(doc)); err == nil {
				t.Fatalf("steady-state variant was accepted: %s", test.behavior)
			}
		})
	}
}

func TestPerformanceBudgetRejectsUnboundedRendererVariants(t *testing.T) {
	for _, test := range []struct {
		name     string
		behavior string
	}{
		{"duplicate grouping", `groupMatches ( response.matches );`},
		{"second fragment", `const spare = document.createDocumentFragment ();`},
		{"incremental result append", `$ ( "results" ).append(fragment);`},
		{"geometry read", `viewport.getBoundingClientRect ();`},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := strings.Replace(validPerformanceDocument, `$("results").replaceChildren(fragment);`, test.behavior+`$("results").replaceChildren(fragment);`, 1)
			if err := performanceBudgetError([]byte(doc)); err == nil {
				t.Fatalf("renderer variant was accepted: %s", test.behavior)
			}
		})
	}
}

func performanceBudgetError(doc []byte) error {
	if len(doc) >= 40<<10 {
		return fmt.Errorf("document bytes=%d, want less than %d", len(doc), 40<<10)
	}

	tags := parseStartTags(string(doc))
	styleCount, scriptCount := 0, 0
	for _, tag := range tags {
		switch tag.name {
		case "style":
			styleCount++
		case "script":
			scriptCount++
			if _, external := tag.attributes["src"]; external {
				return fmt.Errorf("console contains an external script")
			}
		case "link":
			rel := strings.Fields(tag.attributes["rel"])
			as := tag.attributes["as"]
			if containsAny(rel, "stylesheet", "icon") || containsAny([]string{as}, "style", "font", "image") {
				return fmt.Errorf("console contains an external style, font, or image resource")
			}
		case "img", "picture", "source", "image":
			return fmt.Errorf("console contains an external image resource")
		}
	}
	if styleCount != 1 {
		return fmt.Errorf("style blocks=%d, want 1", styleCount)
	}
	if scriptCount != 1 {
		return fmt.Errorf("script blocks=%d, want 1", scriptCount)
	}

	styles, err := elementBody(string(doc), "style")
	if err != nil {
		return err
	}
	if err := steadyStateStyleError(styles); err != nil {
		return err
	}
	script, err := elementBody(string(doc), "script")
	if err != nil {
		return err
	}
	if err := steadyStateScriptError(script); err != nil {
		return err
	}
	if !normalizedContains(script, `state.controller.abort()`) {
		return fmt.Errorf("console is missing request cancellation")
	}
	renderer, err := functionBody(script, "function renderResults(response){", "function blobURL(")
	if err != nil {
		return err
	}
	if err := rendererBudgetError(renderer); err != nil {
		return err
	}
	return nil
}

type htmlStartTag struct {
	name       string
	attributes map[string]string
}

var (
	startTagPattern             = regexp.MustCompile(`(?is)<\s*([a-z][a-z0-9:-]*)\b([^>]*)>`)
	attributePattern            = regexp.MustCompile(`(?is)([a-z_:][a-z0-9_:.-]*)\s*(?:=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>]+)))?`)
	listenerPattern             = regexp.MustCompile(`(?i)\baddEventListener\s*\(\s*["']\s*(scroll|resize)\s*["']`)
	resultRulePattern           = regexp.MustCompile(`(?is)([^{}]+)\{([^{}]*)\}`)
	animationDeclarationPattern = regexp.MustCompile(`(?i)(?:^|;)\s*animation(?:-name)?\s*:`)
	transitionOpacityPattern    = regexp.MustCompile(`(?i)(?:^|;)\s*transition(?:-property)?\s*:[^;]*opacity`)
	resultAccessPattern         = regexp.MustCompile(`(?i)\$\s*\(\s*["']results["']\s*\)\s*\.\s*([a-z][a-z0-9]*)\s*\(`)
	finalResultsPattern         = regexp.MustCompile(`(?i)\$\s*\(\s*["']results["']\s*\)\s*\.\s*replaceChildren\s*\(\s*fragment\s*\)\s*;?`)
	resultsLiteralPattern       = regexp.MustCompile(`["']#?results["']`)
	localFragmentPattern        = regexp.MustCompile(`(?i)\b(?:const|let)\s+fragment\s*=\s*document\s*\.\s*createDocumentFragment\s*\(\s*\)`)
)

func parseStartTags(doc string) []htmlStartTag {
	matches := startTagPattern.FindAllStringSubmatch(doc, -1)
	tags := make([]htmlStartTag, 0, len(matches))
	for _, match := range matches {
		attributes := make(map[string]string)
		for _, attribute := range attributePattern.FindAllStringSubmatch(match[2], -1) {
			value := attribute[2]
			if value == "" {
				value = attribute[3]
			}
			if value == "" {
				value = attribute[4]
			}
			attributes[strings.ToLower(attribute[1])] = strings.ToLower(strings.TrimSpace(value))
		}
		tags = append(tags, htmlStartTag{name: strings.ToLower(match[1]), attributes: attributes})
	}
	return tags
}

func containsAny(values []string, wants ...string) bool {
	for _, value := range values {
		for _, want := range wants {
			if strings.EqualFold(value, want) {
				return true
			}
		}
	}
	return false
}

func elementBody(doc, name string) (string, error) {
	pattern := regexp.MustCompile(`(?is)<\s*` + regexp.QuoteMeta(name) + `\b[^>]*>(.*?)<\s*/\s*` + regexp.QuoteMeta(name) + `\s*>`)
	match := pattern.FindStringSubmatch(doc)
	if len(match) != 2 {
		return "", fmt.Errorf("console is missing inline %s content", name)
	}
	return match[1], nil
}

func steadyStateStyleError(styles string) error {
	compact := compactSource(styles)
	if strings.Contains(compact, "@import") || strings.Contains(compact, "url(") {
		return fmt.Errorf("console contains an external style, font, or image resource")
	}
	if strings.Contains(compact, "sourcemappingurl") {
		return fmt.Errorf("console contains a source map reference")
	}
	if strings.Contains(compact, "@keyframes") {
		return fmt.Errorf("console contains result animation keyframes")
	}
	for _, rule := range resultRulePattern.FindAllStringSubmatch(styles, -1) {
		selector := strings.ToLower(rule[1])
		if !containsResultSelector(selector) {
			continue
		}
		if animationDeclarationPattern.MatchString(rule[2]) || transitionOpacityPattern.MatchString(rule[2]) {
			return fmt.Errorf("console contains a result animation")
		}
	}
	return nil
}

func containsResultSelector(selector string) bool {
	for _, resultSelector := range []string{
		"#results", ".results-panel", ".repository-group", ".file-result",
		".match-block", ".code-row", ".result",
	} {
		if strings.Contains(selector, resultSelector) {
			return true
		}
	}
	return false
}

func steadyStateScriptError(script string) error {
	for _, call := range []string{"setInterval", "setTimeout", "requestAnimationFrame"} {
		pattern := regexp.MustCompile(`(?i)\b` + call + `\s*\(`)
		if pattern.MatchString(script) {
			return fmt.Errorf("console contains steady-state %s work", call)
		}
	}
	if listenerPattern.MatchString(script) {
		return fmt.Errorf("console contains a scroll or resize listener")
	}
	if regexp.MustCompile(`(?i)sourceMappingURL`).MatchString(script) {
		return fmt.Errorf("console contains a source map reference")
	}
	return nil
}

func functionBody(script, start, next string) (string, error) {
	startAt := strings.Index(script, start)
	if startAt < 0 {
		return "", fmt.Errorf("console is missing %s", start)
	}
	endAt := strings.Index(script[startAt+len(start):], next)
	if endAt < 0 {
		return "", fmt.Errorf("console is missing stable boundary %s", next)
	}
	endAt += startAt + len(start)
	return script[startAt:endAt], nil
}

func rendererBudgetError(renderer string) error {
	compact := compactSource(renderer)
	if got := strings.Count(compact, "groupmatches(response.matches)"); got != 1 {
		return fmt.Errorf("renderResults group passes=%d, want 1", got)
	}
	if got := strings.Count(compact, "document.createdocumentfragment()"); got != 1 {
		return fmt.Errorf("renderResults detached fragments=%d, want 1", got)
	}
	if !localFragmentPattern.MatchString(renderer) {
		return fmt.Errorf("renderResults is missing its local detached fragment")
	}
	if got := strings.Count(compact, "code.textcontent=text"); got != 1 {
		return fmt.Errorf("renderResults safe code writes=%d, want 1", got)
	}
	for _, geometryRead := range []string{
		"getboundingclientrect(", "getclientrects(", "offsetwidth", "offsetheight",
		"clientwidth", "clientheight", "scrollwidth", "scrollheight", "scrolltop",
		"scrollleft", "innerwidth", "innerheight",
	} {
		if strings.Contains(compact, geometryRead) {
			return fmt.Errorf("renderResults contains geometry read %s", geometryRead)
		}
	}
	accesses := resultAccessPattern.FindAllStringSubmatch(renderer, -1)
	if len(accesses) != 1 || !strings.EqualFold(accesses[0][1], "replaceChildren") {
		return fmt.Errorf("renderResults mutates #results outside one final replaceChildren call")
	}
	if got := len(resultsLiteralPattern.FindAllString(renderer, -1)); got != 1 {
		return fmt.Errorf("renderResults direct #results accesses=%d, want 1", got)
	}
	final := finalResultsPattern.FindStringIndex(renderer)
	if final == nil || strings.TrimSpace(renderer[final[1]:]) != "}" {
		return fmt.Errorf("renderResults replacement is not its final operation")
	}
	return nil
}

func compactSource(source string) string {
	return strings.ToLower(strings.Join(strings.Fields(source), ""))
}

func normalizedContains(source, want string) bool {
	return strings.Contains(compactSource(source), compactSource(want))
}

const validPerformanceDocument = `<!doctype html>
<html><head><style>#results{display:block}</style></head><body>
<div id="results"></div><script>
function renderResults(response){
const fragment=document.createDocumentFragment(),groups=groupMatches(response.matches);
const code=document.createElement("code");code.textContent=text;fragment.append(code);
$("results").replaceChildren(fragment);
}
function blobURL(match){return match.path}
if(state.controller)state.controller.abort();
</script></body></html>`
