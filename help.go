package mcpcommands

// Rendering help text: compact, CLI-style, meant to be read by a model.

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
)

const usage = "Run a command with run(command, args?). The command is one line, like a shell: " +
	`the command path, then positional arguments, then --options, e.g. issue create "Fix login" --priority 1. ` +
	"Values are parsed as JSON when the parameter is not a string. " +
	`Repeat an array option, or pass a JSON array (--tags '["a","b"]'). ` +
	"Structured values can go in args instead, as an object keyed by parameter name."

func render(node any, path []string, depth int) string {
	if c, ok := node.(*Command); ok {
		return renderCommand(c, path)
	}
	return renderGroup(node.(*Group), path, depth)
}

func renderGroup(g *Group, path []string, depth int) string {
	title := strings.Join(path, " ")
	if title == "" {
		title = g.Name
	}
	out := []string{title}
	if g.Help != "" {
		out[0] = title + ": " + g.Help
	}
	if g.Description != "" {
		out = append(out, "", g.Description)
	}
	if len(g.children) == 0 {
		return strings.Join(append(out, "", "(no commands)"), "\n")
	}

	var rows [][2]string
	listing(g, 0, max(depth, 1), &rows)
	width := 0
	for _, r := range rows {
		width = max(width, utf8.RuneCountInString(r[0]))
	}
	width = min(width, 40)
	out = append(out, "", "Commands:")
	for _, r := range rows {
		if r[1] == "" {
			out = append(out, "  "+r[0])
		} else {
			out = append(out, fmt.Sprintf("  %-*s  %s", width, r[0], r[1]))
		}
	}
	prefix := strings.Join(path, " ")
	if prefix != "" {
		prefix += " "
	}
	out = append(out, "", fmt.Sprintf(`Details: help("%s<command>")`, prefix))
	return strings.Join(out, "\n")
}

func listing(g *Group, level, depth int, rows *[][2]string) {
	pad := strings.Repeat("  ", level)
	for _, child := range g.children {
		switch c := child.(type) {
		case *Group:
			n := count(c)
			noun := "commands"
			if n == 1 {
				noun = "command"
			}
			right := fmt.Sprintf("(%d %s)", n, noun)
			if c.Help != "" {
				right = c.Help + " " + right
			}
			*rows = append(*rows, [2]string{pad + c.Name + " …", right})
			if depth > 1 {
				listing(c, level+1, depth-1, rows)
			}
		case *Command:
			*rows = append(*rows, [2]string{pad + c.Name + briefUsage(c), c.Help})
		}
	}
}

func count(g *Group) int {
	n := 0
	for _, c := range g.children {
		if sub, ok := c.(*Group); ok {
			n += count(sub)
		} else {
			n++
		}
	}
	return n
}

func briefUsage(c *Command) string {
	var b strings.Builder
	for _, p := range c.Params {
		if p.Positional {
			b.WriteString(" " + positional(p))
		}
	}
	return b.String()
}

func positional(p *Param) string {
	dots := ""
	if p.isArray() {
		dots = "..."
	}
	if p.Required {
		return "<" + p.Name + dots + ">"
	}
	return "[" + p.Name + dots + "]"
}

func renderCommand(c *Command, path []string) string {
	u := strings.Join(path, " ") + briefUsage(c)
	if slices.ContainsFunc(c.Params, func(p *Param) bool { return !p.Positional }) {
		u += " [options]"
	}
	out := []string{"Usage: " + u}
	if len(c.Aliases) > 0 {
		out = append(out, "Aliases: "+strings.Join(c.Aliases, ", "))
	}
	if c.Help != "" {
		out = append(out, "", c.Help)
	}
	if c.Description != "" {
		out = append(out, "", c.Description)
	}

	for _, section := range []struct {
		heading    string
		positional bool
	}{{"Arguments", true}, {"Options", false}} {
		var params []*Param
		var labels []string
		width := 0
		for _, p := range c.Params {
			if p.Positional != section.positional {
				continue
			}
			label := p.Flag()
			if p.Positional {
				label = p.Name
			}
			params = append(params, p)
			labels = append(labels, label)
			width = max(width, len(label))
		}
		if len(params) == 0 {
			continue
		}
		width = min(width, 24)
		out = append(out, "", section.heading+":")
		for i, p := range params {
			line := fmt.Sprintf("  %-*s  %s", width, labels[i], paramInfo(p))
			if p.Schema.Description != "" {
				line += "  " + p.Schema.Description
			}
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func paramInfo(p *Param) string {
	s := p.Schema
	info := typeLabel(s)
	if p.Required {
		info += ", required"
	} else if s.Default != nil && string(s.Default) != "null" {
		info += ", default " + string(s.Default)
	}
	var constraints []string
	// Constraints of an array's items are shown too, since that is where tags put them.
	for _, t := range []*jsonschema.Schema{s, s.Items} {
		if t == nil {
			continue
		}
		for _, b := range []struct {
			v    *float64
			word string
		}{{t.Minimum, ">="}, {t.Maximum, "<="}, {t.ExclusiveMinimum, ">"}, {t.ExclusiveMaximum, "<"}} {
			if b.v != nil && math.Abs(*b.v) < 1<<53 {
				constraints = append(constraints, fmt.Sprintf("%s %v", b.word, *b.v))
			}
		}
		if t.MinLength != nil {
			constraints = append(constraints, fmt.Sprintf("min length %d", *t.MinLength))
		}
		if t.MaxLength != nil {
			constraints = append(constraints, fmt.Sprintf("max length %d", *t.MaxLength))
		}
		if t.Pattern != "" {
			constraints = append(constraints, fmt.Sprintf("matches /%s/", t.Pattern))
		}
	}
	if len(constraints) > 0 {
		info += ", " + strings.Join(constraints, ", ")
	}
	return "(" + info + ")"
}

func typeLabel(s *jsonschema.Schema) string {
	if s == nil {
		return "any"
	}
	if len(s.Enum) > 0 {
		parts := make([]string, len(s.Enum))
		for i, v := range s.Enum {
			if str, ok := v.(string); ok {
				parts[i] = str
			} else {
				b, _ := json.Marshal(v)
				parts[i] = string(b)
			}
		}
		return strings.Join(parts, "|")
	}
	if s.Const != nil {
		b, _ := json.Marshal(*s.Const)
		return string(b)
	}
	if branches := append(slices.Clone(s.AnyOf), s.OneOf...); len(branches) > 0 {
		var labels []string
		for _, b := range branches {
			if b.Type == "null" {
				continue
			}
			if l := typeLabel(b); !slices.Contains(labels, l) {
				labels = append(labels, l)
			}
		}
		if len(labels) == 0 {
			return "null"
		}
		return strings.Join(labels, "|")
	}
	t := s.Type
	if t == "" {
		var types []string
		for _, x := range s.Types {
			if x != "null" {
				types = append(types, x)
			}
		}
		if len(types) != 1 {
			if len(types) == 0 {
				return "any"
			}
			return strings.Join(types, "|")
		}
		t = types[0]
	}
	switch {
	case t == "array":
		item := typeLabel(s.Items)
		if strings.Contains(item, "|") {
			return "(" + item + ")[]"
		}
		return item + "[]"
	case t == "object" && len(s.Properties) > 0:
		keys := s.PropertyOrder
		if len(keys) == 0 {
			for k := range s.Properties {
				keys = append(keys, k)
			}
			slices.Sort(keys)
		}
		fields := make([]string, len(keys))
		for i, k := range keys {
			opt := "?"
			if slices.Contains(s.Required, k) {
				opt = ""
			}
			fields[i] = k + opt + ": " + typeLabel(s.Properties[k])
		}
		return "{" + strings.Join(fields, ", ") + "}"
	}
	return t
}
