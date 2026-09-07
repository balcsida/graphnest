package graphquery

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

type DiscoveryQuery struct {
	Text                           string
	Kinds, Languages, Paths, Names []string
}

var discoveryTokens = regexp.MustCompile(`(?:[^\s"]|"[^"]*"|"[^"]*$)+`)

const discoveryKinds = " file module class struct interface trait protocol function method property field variable constant enum enum_member type_alias namespace parameter import export route component union "
const discoveryLanguages = " typescript javascript tsx jsx arkts python go rust java c cpp csharp razor php ruby swift kotlin dart svelte vue astro liquid pascal scala lua luau objc r solidity nix yaml twig xml properties cfml cfscript cfquery cobol vbnet erlang terraform unknown "
const discoveryStopWords = " the a an and or but in on at to for of with by from is it that this are was be has had have do does did will would could should may might can shall not no all each every how what where when who which why i me my we our you your he she they show give tell been done made used using work works found also into then than just more some such over only out its so up as if look need needs want happen happens affect affected break breaks failing implemented implement code file files function method class type fix bug called "

func ParseDiscoveryQuery(raw string) DiscoveryQuery {
	p := DiscoveryQuery{}
	text := []string{}
	for _, token := range discoveryTokens.FindAllString(raw, -1) {
		key, value, ok := strings.Cut(token, ":")
		if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			value = value[1 : len(value)-1]
		}
		handled := ok && value != ""
		switch strings.ToLower(key) {
		case "kind":
			if strings.Contains(discoveryKinds, " "+value+" ") {
				p.Kinds = append(p.Kinds, value)
			} else {
				handled = false
			}
		case "lang", "language":
			value = strings.ToLower(value)
			if strings.Contains(discoveryLanguages, " "+value+" ") {
				p.Languages = append(p.Languages, value)
			} else {
				handled = false
			}
		case "path":
			if handled {
				p.Paths = append(p.Paths, NormalizeDiscovery(value))
			}
		case "name":
			if handled {
				p.Names = append(p.Names, NormalizeDiscovery(value))
			}
		default:
			handled = false
		}
		if !handled {
			text = append(text, token)
		}
	}
	p.Text = strings.Join(text, " ")
	return p
}

// NormalizeDiscovery changes the disposable search projection only. Original
// protobuf facts and bytea selectors retain all bytes, including NUL.
func NormalizeDiscovery(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		if r == 0 {
			return ' '
		}
		return unicode.ToLower(r)
	}, norm.NFD.String(value))
}

func discoveryWords(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func discoverySegments(value string) []string {
	var out []string
	for _, word := range discoveryWords(value) {
		runes := []rune(word)
		start := 0
		for i := 1; i < len(runes); i++ {
			if unicode.IsUpper(runes[i]) && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) || (unicode.IsUpper(runes[i-1]) && i+1 < len(runes) && unicode.IsLower(runes[i+1]))) {
				out = append(out, NormalizeDiscovery(string(runes[start:i])))
				start = i
			}
		}
		out = append(out, NormalizeDiscovery(string(runes[start:])))
	}
	return out
}

func discoveryVariants(word string) []string {
	out := []string{word}
	add := func(v string) {
		if len(v) >= 3 && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	if strings.HasSuffix(word, "ing") && len(word) > 5 {
		b := word[:len(word)-3]
		add(b)
		add(b + "e")
		if len(b) > 1 && b[len(b)-1] == b[len(b)-2] {
			add(b[:len(b)-1])
		}
	}
	if (strings.HasSuffix(word, "tion") || strings.HasSuffix(word, "sion")) && len(word) > 5 {
		add(word[:len(word)-3])
	}
	if strings.HasSuffix(word, "ment") && len(word) > 6 {
		add(word[:len(word)-4])
	}
	if strings.HasSuffix(word, "ies") && len(word) > 4 {
		add(word[:len(word)-3] + "y")
	} else if strings.HasSuffix(word, "es") && len(word) > 4 {
		add(word[:len(word)-2])
		add(word[:len(word)-1])
	} else if strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") && len(word) > 4 {
		add(word[:len(word)-1])
	}
	if strings.HasSuffix(word, "ed") && !strings.HasSuffix(word, "eed") && len(word) > 4 {
		add(word[:len(word)-1])
		add(word[:len(word)-2])
	}
	if strings.HasSuffix(word, "er") && len(word) > 4 {
		b := word[:len(word)-2]
		add(b)
		add(b + "e")
		if b[len(b)-1] == b[len(b)-2] {
			add(b[:len(b)-1])
		}
	}
	return out
}

func discoveryProjectToken(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, NormalizeDiscovery(value))
}

func DiscoveryTerms(value string, projectTerms []string) []string {
	var out []string
	for _, word := range strings.Fields(value) {
		if slices.Contains(projectTerms, discoveryProjectToken(word)) {
			continue
		}
		for _, term := range append(discoveryWords(NormalizeDiscovery(word)), discoverySegments(word)...) {
			if len(term) < 2 || strings.Contains(discoveryStopWords, " "+term+" ") {
				continue
			}
			for _, variant := range discoveryVariants(term) {
				if !slices.Contains(out, variant) {
					out = append(out, variant)
				}
			}
		}
	}
	return out
}

// Search lexemes stay below PostgreSQL's 2047-byte limit. Long words retain an
// exact hash term and use the indexed gram path for prefix/substring retrieval.
func DiscoveryLexeme(term string) string {
	if len(term) <= 1000 {
		return term
	}
	hash := sha256.Sum256([]byte(term))
	return "long" + hex.EncodeToString(hash[:])
}

func DiscoveryDocument(value string) string {
	words := append(discoveryWords(NormalizeDiscovery(value)), discoverySegments(value)...)
	for i, word := range words {
		words[i] = DiscoveryLexeme(word)
	}
	return strings.Join(words, " ")
}

// Native GIN array containment supplies substring recall without pg_trgm, also
// for long lexemes. SQL rechecks full normalized fields after the gram probe.
func DiscoveryGrams(value string) []string {
	runes := []rune(NormalizeDiscovery(value))
	seen := map[string]bool{}
	out := []string{}
	for _, width := range []int{1, 2, 3} {
		for i := 0; i+width <= len(runes); i++ {
			g := string(runes[i : i+width])
			if !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	return out
}

// DiscoveryPatterns compiles gitignore-style rules to PostgreSQL/Go compatible
// regular expressions. Matching order and ignored ancestors are handled by SQL.
func DiscoveryPatterns(patterns []string) ([]string, []bool) {
	expressions := []string{}
	negated := []bool{}
	for _, raw := range patterns {
		pattern := strings.TrimSpace(NormalizeDiscovery(raw))
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}
		negative := strings.HasPrefix(pattern, "!")
		if negative {
			pattern = pattern[1:]
		}
		if pattern == "" {
			continue
		}
		directory := strings.HasSuffix(pattern, "/")
		pattern = strings.TrimSuffix(pattern, "/")
		rooted := strings.Contains(pattern, "/")
		pattern = strings.TrimPrefix(pattern, "/")
		var expression strings.Builder
		if rooted {
			expression.WriteString("^")
		} else {
			expression.WriteString("(^|/)")
		}
		for i := 0; i < len(pattern); i++ {
			switch pattern[i] {
			case '\\':
				if i+1 < len(pattern) {
					i++
					expression.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
				}
			case '*':
				if i+1 < len(pattern) && pattern[i+1] == '*' && (i == 0 || pattern[i-1] == '/') && (i+2 == len(pattern) || pattern[i+2] == '/') {
					i++
					if i+1 < len(pattern) && pattern[i+1] == '/' {
						expression.WriteString("([^/]+/)*")
						i++
					} else {
						expression.WriteString(".+")
					}
				} else {
					if (i == 0 || pattern[i-1] == '/') && (i+1 == len(pattern) || pattern[i+1] == '/') {
						expression.WriteString("[^/]+")
					} else {
						expression.WriteString("[^/]*")
					}
				}
			case '?':
				expression.WriteString("[^/]")
			case '[':
				end := i + 1
				for end < len(pattern) {
					if pattern[end] == ']' {
						if _, err := regexp.Compile(pattern[i : end+1]); err == nil {
							break
						}
					}
					end++
				}
				if end == len(pattern) {
					expression.WriteString(`\[`)
				} else {
					class := pattern[i : end+1]
					if len(class) > 2 && class[1] == '!' {
						class = "[^" + class[2:]
					}
					expression.WriteString(class)
					i = end
				}
			default:
				expression.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
			}
		}
		if directory {
			expression.WriteString("/$")
		} else {
			expression.WriteString("/?$")
		}
		value := expression.String()
		if _, err := regexp.Compile(value); err != nil {
			continue
		}
		expressions = append(expressions, value)
		negated = append(negated, negative)
	}
	return expressions, negated
}
