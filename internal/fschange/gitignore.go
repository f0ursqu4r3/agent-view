package fschange

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// GitignoreMatcher answers "would git ignore this path?" for paths under
// root. Rules come from the root .gitignore, .git/info/exclude (only when
// .git is a directory) and the .gitignore of every directory between the
// root and the path. Each file applies relative to its own directory. Rules
// are cached per directory. It is safe for concurrent use.
type GitignoreMatcher struct {
	root  string
	mu    sync.Mutex
	cache map[string]*ignoreRules // nil: the directory has no rules file
}

// NewGitignoreMatcher returns a matcher for the paths under root.
func NewGitignoreMatcher(root string) *GitignoreMatcher {
	return &GitignoreMatcher{root: root, cache: map[string]*ignoreRules{}}
}

// Invalidate drops the cached rules of the directory that holds
// gitignorePath.
func (m *GitignoreMatcher) Invalidate(gitignorePath string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cache, filepath.Dir(gitignorePath))
}

// IsIgnored reports whether git would ignore abs. A path outside the root,
// or the root itself, is not ignored.
func (m *GitignoreMatcher) IsIgnored(abs string) bool {
	rel, err := filepath.Rel(m.root, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return false
	}
	segments := strings.Split(rel, string(filepath.Separator))
	ignored := false
	dir := m.root
	// Shallow to deep: a deeper file overrides a shallower one, as in git.
	for i := range segments {
		if rules := m.rulesFor(dir); rules != nil {
			if ig, unig := rules.test(strings.Join(segments[i:], "/")); ig {
				ignored = true
			} else if unig {
				ignored = false
			}
		}
		if i < len(segments)-1 {
			dir = filepath.Join(dir, segments[i])
		}
	}
	return ignored
}

func (m *GitignoreMatcher) rulesFor(dir string) *ignoreRules {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rules, ok := m.cache[dir]; ok {
		return rules
	}
	var rules *ignoreRules
	add := func(file string) {
		text, err := os.ReadFile(file)
		if err != nil {
			return
		}
		if rules == nil {
			rules = &ignoreRules{}
		}
		rules.add(string(text))
	}
	add(filepath.Join(dir, ".gitignore"))
	if dir == m.root && isDirectory(filepath.Join(dir, ".git")) {
		add(filepath.Join(dir, ".git", "info", "exclude"))
	}
	m.cache[dir] = rules
	return rules
}

func isDirectory(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// The rest of this file ports node_modules/ignore/index.js 7.0.16 with its
// default options (ignoreCase true), for the two calls the TypeScript makes:
// add(text) and test(path). The names follow that file.
//
// Go's regexp has no lookaround, so the expression differs from the
// JavaScript one where that one uses it, with the same matches:
//   - (?=$|\/$) only ever ends the expression, so it is consumed as (?:$|\/$);
//   - (?!\/)[...], a bracket expression that could match "/", becomes the
//     same class without "/";
//   - pinWildcards is left out: it only stops the JavaScript engine from
//     backtracking and does not change which paths match.
//
// Known differences: JavaScript matches UTF-16 code units and folds case
// with toUpperCase, Go matches code points and folds with Unicode simple
// folding. They differ only for a ? or a bracket expression against a
// character outside the BMP, and for a few non-ASCII case pairs (such as
// U+017F and s).

const (
	bom = "\uFEFF"
	// placeholder brackets the index of a held source (extractBrackets).
	placeholder        = "\x00"
	trailingWildcard   = "\uE000"
	trailingDoublestar = "\uE001"
	wildcard           = `[^\/]*`
	// endOfPath is (?=$|\/$) in the JavaScript.
	endOfPath = `(?:$|\/$)`
	// neverMatch is the empty class [] of the JavaScript.
	neverMatch = `[^\x00-\x{10FFFF}]`
	// regexLiteralSpecial is REGEX_LITERAL_SPECIAL.
	regexLiteralSpecial = `.*+?()[]{}^$|\/`
)

var (
	anchoredGlobstar = regexp.MustCompile(`^/\*{2,}$`)
	restorePattern   = regexp.MustCompile(`\x00([0-9]+)\x00`)
)

// ignoreRules is one ignore() instance: its rules in the order added.
type ignoreRules struct{ rules []ignoreRule }

type ignoreRule struct {
	negative bool
	re       *regexp.Regexp // nil: the rule matches nothing
}

// add follows Ignore.add with a string: one rule per non-empty line.
func (r *ignoreRules) add(text string) {
	if !utf8.ValidString(text) {
		// Node reads the file with U+FFFD for invalid bytes, and Go's
		// regexp rejects invalid UTF-8.
		text = string([]rune(text))
	}
	stripBOM := strings.HasPrefix(text, bom)
	for i, line := range splitPattern(text) {
		if rule, ok := createRule(line, stripBOM && i == 0); ok {
			r.rules = append(r.rules, rule)
		}
	}
}

// test follows Ignore.test (_t): a path whose parent directory is ignored
// stays ignored; otherwise the last rule that matches the path decides.
func (r *ignoreRules) test(path string) (ignored, unignored bool) {
	for i := 0; i < len(path)-1; i++ {
		if path[i] == '/' {
			if ig, _ := r.testOne(path[:i+1]); ig {
				return true, false
			}
		}
	}
	return r.testOne(path)
}

// testOne follows RuleManager.test.
func (r *ignoreRules) testOne(path string) (ignored, unignored bool) {
	for i := len(r.rules) - 1; i >= 0; i-- {
		rule := r.rules[i]
		if rule.re != nil && rule.re.MatchString(path) {
			return !rule.negative, rule.negative
		}
	}
	return false, false
}

// splitPattern drops one final CR, splits at LF and CRLF, and drops empty
// lines.
func splitPattern(text string) []string {
	text = strings.TrimSuffix(text, "\r")
	parts := strings.Split(text, "\n")
	var lines []string
	for i, line := range parts {
		if i < len(parts)-1 {
			line = strings.TrimSuffix(line, "\r")
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// createRule follows createRule for a line of a string: false for a
// comment, a blank pattern or one that ends on an unpaired backslash.
func createRule(pattern string, stripBOM bool) (ignoreRule, bool) {
	body := pattern
	if stripBOM {
		body = strings.TrimPrefix(body, bom)
	}
	if strings.HasPrefix(body, "#") {
		return ignoreRule{}, false
	}
	negative := false
	if strings.HasPrefix(body, "!") {
		negative = true
		body = body[1:]
	}
	if strings.HasSuffix(body, " ") {
		body = trimEnd(body)
	}
	if body == "" || hasTrailingBackslash(body) {
		return ignoreRule{}, false
	}
	if rest, ok := strings.CutPrefix(body, `\!`); ok {
		body = "!" + rest
	}
	if rest, ok := strings.CutPrefix(body, `\#`); ok {
		body = "#" + rest
	}
	// Every source compiles (checked against ignore 7.0.16 on random
	// patterns); one that did not would match nothing.
	re, _ := regexp.Compile("(?i)" + makeRegex(makeRegexPrefix(body)))
	return ignoreRule{negative: negative, re: re}, true
}

// trimEnd drops trailing spaces; a space escaped by an odd run of
// backslashes stays, unescaped, as the last one.
func trimEnd(body string) string {
	end := len(body)
	for end > 0 && body[end-1] == ' ' {
		end--
	}
	backslashes := 0
	for end-backslashes-1 >= 0 && body[end-backslashes-1] == '\\' {
		backslashes++
	}
	if backslashes%2 == 1 {
		return body[:end-1] + " "
	}
	return body[:end]
}

func hasTrailingBackslash(s string) bool {
	count := 0
	for count < len(s) && s[len(s)-count-1] == '\\' {
		count++
	}
	return count%2 == 1
}

// makeRegexPrefix follows makeRegexPrefix: it runs the REPLACERS chain on
// the pattern with its bracket expressions held aside, then puts them back.
func makeRegexPrefix(pattern string) string {
	if anchoredGlobstar.MatchString(pattern) {
		pattern = "**"
	}
	source, sources := extractBrackets(pattern)
	for _, step := range []func(string) string{
		unescapeSpaces,
		foldStarRuns,
		escapeMetas,
		replaceQuestionMarks,
		replaceLeadingSlash,
		escapeSlashes,
		replaceLeadingGlobstar,
	} {
		source = step(source)
	}
	source = anchorStart(source, pattern)
	for _, step := range []func(string) string{
		replaceGlobstars,
		replaceWildcards,
		markTrailingWildcard,
		unescapeEscapedMetas,
		unescapeBackslashes,
		// The escaped-bracket step, \\\[([^\]/]*?)($|\]) -> \[$1$2, gives
		// back what it matched, so it is left out.
		anchorEnd,
	} {
		source = step(source)
	}
	if len(sources) == 0 {
		return source
	}
	return restorePattern.ReplaceAllStringFunc(source, func(m string) string {
		i, _ := strconv.Atoi(m[1 : len(m)-1])
		return sources[i]
	})
}

// makeRegex follows IgnoreRule._make in regex mode: it expands the pending
// trailing wildcard and trailing "/**".
func makeRegex(prefix string) string {
	if body, ok := strings.CutSuffix(prefix, trailingWildcard); ok {
		if head, ok := strings.CutSuffix(body, `\/`); ok {
			// 'abc/*' does not match 'abc/'.
			prefix = head + `\/[^/]+` + endOfPath
		} else {
			// 'a*' matches 'a' and 'aa'.
			prefix = body + `[^/]*` + endOfPath
		}
	}
	if body, ok := strings.CutSuffix(prefix, trailingDoublestar); ok {
		// 'abc/**' matches everything under 'abc', but not 'abc/' itself.
		prefix = body + `[\s\S]`
	}
	return prefix
}

func isReserved(c rune) bool {
	return c == 0 || c == '\uE000' || c == '\uE001'
}

// extractBrackets replaces every bracket expression, and every escape but
// \*, \[ and "\ ", with a placeholder that the chain leaves alone, and
// returns the held sources.
func extractBrackets(pattern string) (string, []string) {
	p := []rune(pattern)
	var sources []string
	var out strings.Builder
	hold := func(src string) {
		sources = append(sources, src)
		out.WriteString(placeholder + strconv.Itoa(len(sources)-1) + placeholder)
	}
	for i := 0; i < len(p); {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p):
			escaped := p[i+1]
			switch {
			case escaped == '/' && i+2 == len(p):
				// Git removes a final directory slash before matching, which
				// leaves the escape without a character.
				hold(neverMatch)
			case escaped == '*' || escaped == '[' || escaped == ' ':
				out.WriteString(string(p[i : i+2]))
			case strings.ContainsRune(regexLiteralSpecial, escaped):
				hold(`\` + string(escaped))
			case isReserved(escaped):
				hold(classSource(false, []runeRange{{escaped, escaped}}))
			default:
				hold(string(escaped))
			}
			i += 2
		case isReserved(c):
			hold(classSource(false, []runeRange{{c, c}}))
			i++
		case c == '[':
			src, end, ok := scanBracket(p, i)
			if !ok {
				// git gives up on the whole pattern.
				hold(neverMatch)
				i = len(p)
			} else {
				hold(src)
				i = end + 1
			}
		default:
			out.WriteRune(c)
			i++
		}
	}
	return out.String(), sources
}

type runeRange struct{ lo, hi rune }

// posixClasses are git's ASCII-only POSIX classes, without "/".
var posixClasses = map[string][]runeRange{
	"alnum":  {{'0', '9'}, {'A', 'Z'}, {'a', 'z'}},
	"alpha":  {{'A', 'Z'}, {'a', 'z'}},
	"blank":  {{' ', ' '}, {'\t', '\t'}},
	"cntrl":  {{0, 0x1f}, {0x7f, 0x7f}},
	"digit":  {{'0', '9'}},
	"graph":  {{'!', '.'}, {'0', '~'}},
	"lower":  {{'a', 'z'}},
	"print":  {{' ', '.'}, {'0', '~'}},
	"punct":  {{'!', '.'}, {':', '@'}, {'[', '`'}, {'{', '~'}},
	"space":  {{' ', ' '}, {'\t', '\t'}, {'\n', '\n'}, {'\r', '\r'}},
	"upper":  {{'A', 'Z'}},
	"xdigit": {{'0', '9'}, {'A', 'F'}, {'a', 'f'}},
}

// scanBracket follows scanBracket: it reads the bracket expression that
// starts at p[start] == '[' and returns its source and the index of its
// closing "]". ok is false when the expression is never closed, which
// makes the whole pattern match nothing.
func scanBracket(p []rune, start int) (src string, end int, ok bool) {
	i := start + 1
	negated := false
	if i < len(p) && (p[i] == '!' || p[i] == '^') {
		negated = true
		i++
	}
	var set []runeRange
	// prev is the member a "-" could start a range from.
	var prev rune
	hasPrev := false
	for {
		if i >= len(p) {
			return "", 0, false
		}
		c := p[i]
		switch {
		case c == '\\':
			i++
			if i >= len(p) {
				return "", 0, false
			}
			set = append(set, runeRange{p[i], p[i]})
			prev, hasPrev = p[i], true
		case c == '-' && hasPrev && i+1 < len(p) && p[i+1] != ']':
			i++
			to := p[i]
			if to == '\\' && i+1 < len(p) {
				i++
				to = p[i]
			}
			// An out-of-order range matches nothing in git; its lower
			// bound is already a member.
			if prev <= to {
				set = append(set, runeRange{prev, to})
			}
			hasPrev = false
		case c == '[' && i+1 < len(p) && p[i+1] == ':':
			nameStart := i + 2
			nameEnd := nameStart
			for nameEnd < len(p) && p[nameEnd] != ']' {
				nameEnd++
			}
			if nameEnd == len(p) {
				return "", 0, false
			}
			if nameEnd > nameStart && p[nameEnd-1] == ':' {
				class, known := posixClasses[string(p[nameStart:nameEnd-1])]
				if !known {
					return "", 0, false
				}
				set = append(set, class...)
				hasPrev = false
				i = nameEnd
			} else {
				// No ":]" closes it, so the "[" is a plain member.
				set = append(set, runeRange{'[', '['})
				prev, hasPrev = '[', true
			}
		default:
			set = append(set, runeRange{c, c})
			prev, hasPrev = c, true
		}
		i++
		if i < len(p) && p[i] == ']' {
			return classSource(negated, set), i, true
		}
	}
}

// classSource is the Go class for a bracket expression. It never matches
// "/": a negated class excludes it, and a plain one drops it.
func classSource(negated bool, set []runeRange) string {
	var b strings.Builder
	b.WriteByte('[')
	if negated {
		b.WriteString(`^\/`)
		for _, r := range set {
			writeRange(&b, r)
		}
	} else {
		n := 0
		for _, r := range set {
			for _, part := range withoutSlash(r) {
				writeRange(&b, part)
				n++
			}
		}
		if n == 0 {
			return neverMatch
		}
	}
	b.WriteByte(']')
	return b.String()
}

func withoutSlash(r runeRange) []runeRange {
	if r.lo > '/' || r.hi < '/' {
		return []runeRange{r}
	}
	var parts []runeRange
	if r.lo < '/' {
		parts = append(parts, runeRange{r.lo, '/' - 1})
	}
	if r.hi > '/' {
		parts = append(parts, runeRange{'/' + 1, r.hi})
	}
	return parts
}

func writeRange(b *strings.Builder, r runeRange) {
	fmt.Fprintf(b, `\x{%x}`, r.lo)
	if r.hi != r.lo {
		fmt.Fprintf(b, `-\x{%x}`, r.hi)
	}
}

// replaceAll follows String.prototype.replace with a global expression: it
// scans from the left and replaces each match, which match reports for a
// start at p. No matcher here matches an empty string.
func replaceAll(s string, match func(s string, p int) (end int, repl string, ok bool)) string {
	var b strings.Builder
	last, matched := 0, false
	for p := 0; p < len(s); {
		end, repl, ok := match(s, p)
		if !ok {
			p++
			continue
		}
		b.WriteString(s[last:p])
		b.WriteString(repl)
		last, p, matched = end, end, true
	}
	if !matched {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// unescapeSpaces: (\\+)( ?) — an odd run of backslashes before a space
// loses one, and the space stays.
func unescapeSpaces(s string) string {
	if !strings.Contains(s, `\ `) {
		return s
	}
	return replaceAll(s, func(s string, p int) (int, string, bool) {
		if s[p] != '\\' {
			return 0, "", false
		}
		end := p
		for end < len(s) && s[end] == '\\' {
			end++
		}
		if end == len(s) || s[end] != ' ' {
			return end, s[p:end], true
		}
		run := end - p
		return end + 1, s[p:p+run-run%2] + " ", true
	})
}

// foldStarRuns: (^|\/)\*{3,}(?=\/|$) — a run of three or more asterisks
// that fills a path segment becomes "**".
func foldStarRuns(s string) string {
	if !strings.Contains(s, "*") {
		return s
	}
	run := func(from int, start string) (int, string, bool) {
		end := from
		for end < len(s) && s[end] == '*' {
			end++
		}
		if end-from >= 3 && (end == len(s) || s[end] == '/') {
			return end, start + "**", true
		}
		return 0, "", false
	}
	return replaceAll(s, func(s string, p int) (int, string, bool) {
		if p == 0 {
			if end, repl, ok := run(0, ""); ok {
				return end, repl, ok
			}
		}
		if s[p] == '/' {
			return run(p+1, "/")
		}
		return 0, "", false
	})
}

// escapeMetas: [\\$.|*+(){^] gets a backslash.
func escapeMetas(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(`\$.|*+(){^`, s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// replaceQuestionMarks: every "?" left is a wildcard for one character.
func replaceQuestionMarks(s string) string {
	return strings.ReplaceAll(s, "?", "[^/]")
}

// replaceLeadingSlash: a leading slash anchors to the beginning.
func replaceLeadingSlash(s string) string {
	if rest, ok := strings.CutPrefix(s, "/"); ok {
		return "^" + rest
	}
	return s
}

// escapeSlashes: every "/" gets a backslash.
func escapeSlashes(s string) string {
	return strings.ReplaceAll(s, "/", `\/`)
}

// replaceLeadingGlobstar: ^\^*(?:\\\*\\\*\\\/)+ — a leading "**/" matches
// in all directories.
func replaceLeadingGlobstar(s string) string {
	start := 0
	for start < len(s) && s[start] == '^' {
		start++
	}
	end := start
	for strings.HasPrefix(s[end:], `\*\*\/`) {
		end += len(`\*\*\/`)
	}
	if end == start {
		return s
	}
	return `(?:^|\/)` + s[end:]
}

// anchorStart: a pattern with a slash at its beginning or middle is
// relative to its .gitignore; any other may match at any level.
func anchorStart(s, pattern string) string {
	if s == "" || s[0] == '^' || s[0] == '(' {
		return s
	}
	if len(pattern) > 1 && strings.Contains(pattern[:len(pattern)-1], "/") {
		return "^" + s
	}
	return `(?:^|\/)` + s
}

// replaceGlobstars: \/\*\*(?=\\\/|$) — "/**/" is zero or more
// directories, a trailing "/**/" one or more, and a trailing "/**" is left
// for makeRegex.
func replaceGlobstars(s string) string {
	const globstar = `\/\*\*`
	return replaceAll(s, func(s string, p int) (int, string, bool) {
		if !strings.HasPrefix(s[p:], globstar) {
			return 0, "", false
		}
		end := p + len(globstar)
		switch rest := s[end:]; {
		case rest == "":
			return end, `\/` + trailingDoublestar, true
		case rest == `\/`:
			return end, `(?:\/[^\/]+)+`, true
		case strings.HasPrefix(rest, `\/`):
			return end, `(?:\/[^\/]+)*`, true
		}
		return 0, "", false
	})
}

// replaceWildcards: (^|[^\\])(?:\\\*)+(?!$) — a run of unescaped asterisks
// that does not end the pattern is one wildcard.
func replaceWildcards(s string) string {
	stars := func(from int, p1 string) (int, string, bool) {
		k := 0
		for strings.HasPrefix(s[from+2*k:], `\*`) {
			k++
		}
		// (?!$) gives back the last asterisk when the run ends the source.
		if k > 0 && from+2*k == len(s) {
			k--
		}
		if k == 0 {
			return 0, "", false
		}
		return from + 2*k, p1 + wildcard, true
	}
	return replaceAll(s, func(s string, p int) (int, string, bool) {
		if p == 0 {
			if end, repl, ok := stars(0, ""); ok {
				return end, repl, ok
			}
		}
		if s[p] != '\\' {
			return stars(p+1, s[p:p+1])
		}
		return 0, "", false
	})
}

// markTrailingWildcard: (^|[^\\])((?:\\\\)*)\\\*$ — a final unescaped
// asterisk becomes the marker that makeRegex expands; an escaped one stays.
func markTrailingWildcard(s string) string {
	n := len(s)
	if n < 2 || s[n-1] != '*' || s[n-2] != '\\' || (n >= 3 && s[n-3] == '\\') {
		return s
	}
	return s[:n-2] + trailingWildcard
}

// unescapeEscapedMetas: \\\\\\(?=[$.|*+(){^]) — a user-escaped
// metacharacter goes back to one backslash.
func unescapeEscapedMetas(s string) string {
	return replaceAll(s, func(s string, p int) (int, string, bool) {
		if strings.HasPrefix(s[p:], `\\\`) && p+3 < len(s) && strings.IndexByte(`$.|*+(){^`, s[p+3]) >= 0 {
			return p + 3, `\`, true
		}
		return 0, "", false
	})
}

// unescapeBackslashes: \\\\ -> \\.
func unescapeBackslashes(s string) string {
	return strings.ReplaceAll(s, `\\`, `\`)
}

// anchorEnd: "js" does not match "js." but matches "js/"; a pattern with a
// trailing slash matches only a directory.
func anchorEnd(s string) string {
	switch {
	case s == "", strings.HasSuffix(s, trailingWildcard), strings.HasSuffix(s, trailingDoublestar):
		return s
	case strings.HasSuffix(s, "/"):
		return s + "$"
	}
	return s + endOfPath
}
