package mcpcommands

// Turning a command line (plus an optional args object) into a command and its values.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/google/jsonschema-go/jsonschema"
)

// splitPath splits a help path such as "issue create", "issue/create" or "issue > create".
func splitPath(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return unicode.IsSpace(r) || r == '/' || r == '>' })
}

// tokenize splits a command line like a POSIX shell: quotes, backslash escapes, no expansion.
func tokenize(line string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	inToken := false
	var quote rune
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			} else {
				current.WriteRune(c)
			}
		case quote == '"':
			if c == '"' {
				quote = 0
			} else if c == '\\' && i+1 < len(runes) && strings.ContainsRune("\"\\$`", runes[i+1]) {
				i++
				current.WriteRune(runes[i])
			} else {
				current.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote = c
			inToken = true
		case c == '\\':
			if i+1 >= len(runes) {
				return nil, Errorf("cannot parse command line: no escaped character")
			}
			i++
			current.WriteRune(runes[i])
			inToken = true
		case unicode.IsSpace(c):
			if inToken {
				tokens = append(tokens, current.String())
			}
			current.Reset()
			inToken = false
		default:
			current.WriteRune(c)
			inToken = true
		}
	}
	if quote != 0 {
		return nil, Errorf("cannot parse command line: no closing quotation")
	}
	if inToken {
		tokens = append(tokens, current.String())
	}
	return tokens, nil
}

// walk follows tokens down the tree as far as they name groups and commands.
func walk(root *Group, tokens []string) (node any, path, rest []string) {
	node = root
	i := 0
	for ; i < len(tokens); i++ {
		g, ok := node.(*Group)
		if !ok {
			break
		}
		child := g.lookup(tokens[i])
		if child == nil {
			break
		}
		node = child
		path = append(path, nodeName(child))
	}
	return node, path, tokens[i:]
}

func unknownCommand(g *Group, path []string, token string) error {
	where := strings.Join(path, " ")
	if where == "" {
		where = "the root"
	}
	var names, primary []string
	for _, c := range g.children {
		names = append(names, nodeName(c))
		names = append(names, nodeAliases(c)...)
		primary = append(primary, nodeName(c))
	}
	hint := ""
	if close := closeMatches(token, names, 3); len(close) > 0 {
		for i, c := range close {
			close[i] = strings.Join(append(slices.Clone(path), c), " ")
		}
		hint = fmt.Sprintf(" Did you mean: %s?", strings.Join(close, ", "))
	}
	available := strings.Join(primary, ", ")
	if available == "" {
		available = "none"
	}
	return Errorf("Unknown command %s in %s.%s Available: %s.", jsonString(token), where, hint, available)
}

// bind assigns command-line tokens and the args object to the command's parameters.
func bind(c *Command, tokens []string, args map[string]any) (map[string]any, error) {
	values := map[string]any{}
	var loose []string

	optionsDone := false
	for i := 0; i < len(tokens); {
		token := tokens[i]
		i++
		if optionsDone || !strings.HasPrefix(token, "--") {
			loose = append(loose, token)
			continue
		}
		if token == "--" {
			optionsDone = true
			continue
		}
		key, raw, hasValue := strings.Cut(token[2:], "=")
		p := c.param(key)
		if p == nil && strings.HasPrefix(key, "no-") {
			if negated := c.param(key[3:]); negated != nil && negated.isBool() {
				if err := assign(values, negated, false); err != nil {
					return nil, err
				}
				continue
			}
		}
		if p == nil {
			return nil, unknownOption(c, key)
		}
		if p.isBool() && !hasValue {
			if err := assign(values, p, true); err != nil {
				return nil, err
			}
			continue
		}
		if !hasValue {
			if i >= len(tokens) {
				return nil, Usagef("%s needs a value", p.Flag())
			}
			raw = tokens[i]
			i++
		}
		if err := assign(values, p, coerce(p, raw)); err != nil {
			return nil, err
		}
	}

	var open []*Param
	for _, p := range c.Params {
		if _, set := values[p.Name]; p.Positional && !set {
			open = append(open, p)
		}
	}
	for n, p := range open {
		if len(loose) == 0 {
			break
		}
		if p.isArray() && n == len(open)-1 {
			for _, raw := range loose {
				if err := assign(values, p, coerce(p, raw)); err != nil {
					return nil, err
				}
			}
			loose = nil
		} else {
			if err := assign(values, p, coerce(p, loose[0])); err != nil {
				return nil, err
			}
			loose = loose[1:]
		}
	}
	if len(loose) > 0 {
		quoted := make([]string, len(loose))
		for i, t := range loose {
			quoted[i] = shellQuote(t)
		}
		return nil, Usagef("unexpected argument(s): %s", strings.Join(quoted, " "))
	}

	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p := c.param(key)
		if p == nil {
			return nil, unknownOption(c, key)
		}
		if _, set := values[p.Name]; set {
			return nil, Usagef("%s is given both in the command line and in args", p.Name)
		}
		values[p.Name] = args[key]
	}
	return values, nil
}

func assign(values map[string]any, p *Param, value any) error {
	if p.isArray() {
		list, _ := values[p.Name].([]any)
		if items, ok := value.([]any); ok {
			list = append(list, items...)
		} else {
			list = append(list, value)
		}
		values[p.Name] = list
		return nil
	}
	if _, set := values[p.Name]; set {
		return Usagef("%s is given more than once", p.Name)
	}
	values[p.Name] = value
	return nil
}

// coerce turns a command-line token into a value, guided by the parameter's schema.
//
// Strings stay strings; anything else is read as JSON when it parses, so 3,
// true, [1,2] and {"a":1} arrive typed. A token for an array is one item,
// unless it is a JSON array.
func coerce(p *Param, raw string) any {
	schema := p.Schema
	if p.isArray() {
		if strings.HasPrefix(raw, "[") {
			if parsed, ok := tryJSON(raw).([]any); ok {
				return parsed
			}
		}
		schema = nil
		for _, s := range append([]*jsonschema.Schema{p.Schema}, p.Schema.AnyOf...) {
			if s.Items != nil {
				schema = s.Items
				break
			}
		}
	}
	if schemaTypes(schema)["string"] {
		return raw
	}
	if v := tryJSON(raw); v != nil {
		return v
	}
	return raw
}

func tryJSON(raw string) any {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	return v
}

var shellSafe = regexp.MustCompile(`^[\w@%+=:,./-]+$`)

func shellQuote(token string) string {
	if shellSafe.MatchString(token) {
		return token
	}
	return "'" + strings.ReplaceAll(token, "'", `'"'"'`) + "'"
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func unknownOption(c *Command, key string) error {
	flags := make([]string, len(c.Params))
	for i, p := range c.Params {
		flags[i] = p.Flag()
	}
	hint := ""
	if close := closeMatches("--"+key, flags, 1); len(close) > 0 {
		hint = fmt.Sprintf(" Did you mean %s?", close[0])
	}
	list := strings.Join(flags, ", ")
	if list == "" {
		list = "none"
	}
	return Usagef("unknown option --%s.%s Options: %s.", key, hint, list)
}

func closeMatches(word string, candidates []string, n int) []string {
	type match struct {
		c     string
		score float64
	}
	var matches []match
	for _, c := range candidates {
		score := 1 - float64(distance(word, c))/float64(max(len(word), len(c)))
		if score >= 0.6 {
			matches = append(matches, match{c, score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	var out []string
	for i := 0; i < len(matches) && i < n; i++ {
		out = append(out, matches[i].c)
	}
	return out
}

func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
