package mcpcommands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Handler runs a command. in holds the parsed, validated parameters. req is the
// MCP request, or nil when the command is called through [Server.Execute].
//
// A handler can return a string, any JSON-serializable value, nil (shown as
// "OK"), or a *mcp.CallToolResult. A returned error fails the call with
// exactly its message.
type Handler[In any] func(ctx context.Context, req *mcp.CallToolRequest, in In) (any, error)

// Parent is something commands and groups can be added to: a [*Group] or a [*Server].
type Parent interface {
	group() *Group
}

// Param is one parameter of a command, as the model sees it.
type Param struct {
	Name string
	// Schema is the JSON schema of the value.
	Schema     *jsonschema.Schema
	Required   bool
	Positional bool

	resolved *jsonschema.Resolved
}

// Flag is the option spelling of the parameter, e.g. --dry-run.
func (p *Param) Flag() string { return "--" + kebab(p.Name) }

func (p *Param) isBool() bool {
	types := schemaTypes(p.Schema)
	return len(types) == 1 && types["boolean"]
}

func (p *Param) isArray() bool { return schemaTypes(p.Schema)["array"] }

// Command is a leaf of the tree: a function the model can run.
type Command struct {
	Name string
	// Help is the one-line summary shown in listings; Description is the rest.
	Help        string
	Description string
	Aliases     []string
	Params      []*Param

	invoke func(ctx context.Context, req *mcp.CallToolRequest, values map[string]any) (any, error)
}

func (c *Command) param(key string) *Param {
	for _, p := range c.Params {
		if p.Name == key || p.Flag() == "--"+key || p.Name == strings.ReplaceAll(key, "-", "_") {
			return p
		}
	}
	return nil
}

// Group is an inner node of the tree: a named set of commands and subgroups.
type Group struct {
	Name string
	// Help is the one-line summary shown in listings; Description is the rest.
	Help        string
	Description string
	Aliases     []string

	children []any // *Group or *Command, in registration order
}

func (g *Group) group() *Group { return g }

// Group creates a subgroup. The first paragraph of description is its one-line
// help. It panics if the name is invalid or taken.
func (g *Group) Group(name, description string, aliases ...string) *Group {
	sub := &Group{Name: checkName(name), Aliases: checkNames(aliases)}
	sub.Help, sub.Description = splitDescription(description)
	g.add(sub)
	return sub
}

// Add registers a command under parent. In must be a struct; its exported
// fields are the command's parameters, named by their json tags:
//
//	type CreateInput struct {
//		Title    string   `json:"title" cli:"arg" jsonschema:"Short summary."`
//		Priority int      `json:"priority,omitempty" cli:"arg" default:"2" minimum:"0" maximum:"4"`
//		Draft    bool     `json:"draft,omitempty"`
//	}
//
// Fields tagged cli:"arg" are positional arguments, in field order; the others
// are options, passed as --name value. Fields without omitempty/omitzero (and
// without a default) are required. The jsonschema tag is the description, and
// the default, enum (comma-separated), minimum and maximum tags add those
// constraints. If *In or In has a Validate() error method, it runs after parsing.
//
// The first paragraph of description is the one-liner shown in listings; the
// rest appears on the command's help page. Add panics on invalid definitions.
func Add[In any](parent Parent, name, description string, h Handler[In], aliases ...string) *Command {
	t := reflect.TypeFor[In]()
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("command %s: input type %v is not a struct", name, t))
	}
	params, err := paramsFor(t)
	if err != nil {
		panic(fmt.Sprintf("command %s: %v", name, err))
	}
	c := &Command{Name: checkName(name), Aliases: checkNames(aliases), Params: params}
	c.Help, c.Description = splitDescription(description)
	c.invoke = func(ctx context.Context, req *mcp.CallToolRequest, values map[string]any) (any, error) {
		if err := c.check(values); err != nil {
			return nil, err
		}
		data, err := json.Marshal(values)
		if err != nil {
			return nil, &UsageError{Msg: err.Error()}
		}
		var in In
		if err := json.Unmarshal(data, &in); err != nil {
			return nil, &UsageError{Msg: err.Error()}
		}
		if v, ok := any(&in).(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				var usage *UsageError
				if !errors.As(err, &usage) {
					err = &UsageError{Msg: err.Error()}
				}
				return nil, err
			}
		}
		return h(ctx, req, in)
	}
	parent.group().add(c)
	return c
}

// check fills in defaults and validates each value against its parameter's schema.
func (c *Command) check(values map[string]any) error {
	var problems []string
	for _, p := range c.Params {
		v, ok := values[p.Name]
		if !ok {
			if p.Required {
				problems = append(problems, fmt.Sprintf("missing required argument <%s>", p.Name))
			} else if p.Schema.Default != nil {
				var d any
				if err := json.Unmarshal(p.Schema.Default, &d); err == nil {
					values[p.Name] = d
				}
			}
			continue
		}
		if err := p.resolved.Validate(v); err != nil {
			label := p.Name
			if !p.Positional {
				label = p.Flag()
			}
			found := explain(p.Schema, v, label)
			if found == nil {
				found = []string{fmt.Sprintf("%s: %s (got %s)", label, validationPrefix.ReplaceAllString(err.Error(), ""), describe(v))}
			}
			problems = append(problems, found...)
		}
	}
	if problems != nil {
		return &UsageError{Msg: strings.Join(problems, "; ")}
	}
	return nil
}

var validationPrefix = regexp.MustCompile(`^(validating [^:]*: )+`)

// explain lists why v does not fit s, for the common cases, naming the path to
// each bad value (deps.0, filter.status). It returns nil when it cannot tell.
func explain(s *jsonschema.Schema, v any, where string) []string {
	if msg := explainValue(s, v); msg != "" {
		return []string{fmt.Sprintf("%s: %s (got %s)", where, msg, describe(v))}
	}
	var out []string
	switch x := v.(type) {
	case []any:
		if s.Items != nil {
			for i, item := range x {
				out = append(out, explain(s.Items, item, fmt.Sprintf("%s.%d", where, i))...)
			}
		}
	case map[string]any:
		for _, k := range s.Required {
			if _, ok := x[k]; !ok {
				out = append(out, fmt.Sprintf("%s.%s: missing required property", where, k))
			}
		}
		closed := s.AdditionalProperties != nil && s.AdditionalProperties.Not != nil
		for _, k := range slices.Sorted(maps.Keys(x)) {
			if sub, ok := s.Properties[k]; ok {
				out = append(out, explain(sub, x[k], where+"."+k)...)
			} else if closed {
				names := slices.Sorted(maps.Keys(s.Properties))
				out = append(out, fmt.Sprintf("%s.%s: unknown property. Properties: %s", where, k, strings.Join(names, ", ")))
			}
		}
	}
	return out
}

// describe names the JSON type of v for error messages, with the keys of an
// object, rather than echoing the value back.
func describe(v any) string {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return jsonType(v)
	}
	keys := slices.Sorted(maps.Keys(m))
	if len(keys) > 10 {
		keys = append(keys[:10], "…")
	}
	return "object {" + strings.Join(keys, ", ") + "}"
}

func explainValue(s *jsonschema.Schema, v any) string {
	if want := schemaTypes(s); len(want) > 0 && !want[jsonType(v)] && !(want["number"] && jsonType(v) == "integer") {
		if len(s.Enum) == 0 {
			names := slices.Sorted(maps.Keys(want))
			return "expected " + strings.Join(names, "|")
		}
	}
	if len(s.Enum) > 0 && !slices.ContainsFunc(s.Enum, func(e any) bool { return reflect.DeepEqual(e, v) }) {
		quoted := make([]string, len(s.Enum))
		for i, e := range s.Enum {
			b, _ := json.Marshal(e)
			quoted[i] = string(b)
		}
		return "expected one of " + strings.Join(quoted, "|")
	}
	if f, ok := v.(float64); ok {
		if s.Minimum != nil && f < *s.Minimum {
			return fmt.Sprintf("must be >= %v", *s.Minimum)
		}
		if s.Maximum != nil && f > *s.Maximum {
			return fmt.Sprintf("must be <= %v", *s.Maximum)
		}
	}
	return ""
}

func (g *Group) add(node any) {
	for _, key := range append([]string{nodeName(node)}, nodeAliases(node)...) {
		if g.lookup(key) != nil {
			panic(fmt.Sprintf("%s already has a command or group named %s", g.Name, key))
		}
	}
	g.children = append(g.children, node)
}

func (g *Group) lookup(token string) any {
	for _, c := range g.children {
		if nodeName(c) == token {
			return c
		}
	}
	for _, c := range g.children {
		if slices.Contains(nodeAliases(c), token) {
			return c
		}
	}
	return nil
}

func nodeName(node any) string {
	if g, ok := node.(*Group); ok {
		return g.Name
	}
	return node.(*Command).Name
}

func nodeAliases(node any) []string {
	if g, ok := node.(*Group); ok {
		return g.Aliases
	}
	return node.(*Command).Aliases
}

// paramsFor derives a command's parameters from its input struct.
func paramsFor(t reflect.Type) ([]*Param, error) {
	schema, err := jsonschema.ForType(t, nil)
	if err != nil {
		return nil, err
	}
	tags := fieldTags(t)
	params := make([]*Param, 0, len(schema.PropertyOrder))
	for _, name := range schema.PropertyOrder {
		s := schema.Properties[name]
		tag := tags[name]
		if err := applyTags(s, tag); err != nil {
			return nil, fmt.Errorf("field %s: %w", name, err)
		}
		resolved, err := s.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", name, err)
		}
		params = append(params, &Param{
			Name:       name,
			Schema:     s,
			Required:   slices.Contains(schema.Required, name) && s.Default == nil,
			Positional: tag.Get("cli") == "arg",
			resolved:   resolved,
		})
	}
	return params, nil
}

// fieldTags maps JSON property names to the struct tags of their fields.
func fieldTags(t reflect.Type) map[string]reflect.StructTag {
	tags := map[string]reflect.StructTag{}
	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() || (f.Anonymous && f.Type.Kind() == reflect.Struct) {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		tags[name] = f.Tag
	}
	return tags
}

func applyTags(s *jsonschema.Schema, tag reflect.StructTag) error {
	if raw, ok := tag.Lookup("default"); ok {
		// Strings may be given bare: default:"open".
		if schemaTypes(s)["string"] && !strings.HasPrefix(raw, `"`) {
			quoted, _ := json.Marshal(raw)
			raw = string(quoted)
		} else if !json.Valid([]byte(raw)) {
			return fmt.Errorf("default %q is not valid JSON", raw)
		}
		s.Default = json.RawMessage(raw)
	}
	// Value constraints on an array apply to its items.
	target := s
	if schemaTypes(s)["array"] && s.Items != nil {
		target = s.Items
	}
	if raw, ok := tag.Lookup("enum"); ok {
		isString := schemaTypes(target)["string"]
		for _, item := range strings.Split(raw, ",") {
			item = strings.TrimSpace(item)
			var v any = item
			if !isString {
				if err := json.Unmarshal([]byte(item), &v); err != nil {
					return fmt.Errorf("enum value %q is not valid JSON", item)
				}
			}
			target.Enum = append(target.Enum, v)
		}
	}
	for key, field := range map[string]**float64{"minimum": &target.Minimum, "maximum": &target.Maximum} {
		if raw, ok := tag.Lookup(key); ok {
			f, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return fmt.Errorf("%s %q is not a number", key, raw)
			}
			*field = &f
		}
	}
	return nil
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func checkName(name string) string {
	if !nameRE.MatchString(name) {
		panic(fmt.Sprintf("invalid command name %q: use letters, digits, '-' and '_'", name))
	}
	return name
}

func checkNames(names []string) []string {
	for _, n := range names {
		checkName(n)
	}
	return names
}

var blankLine = regexp.MustCompile(`\n\s*\n`)

func splitDescription(text string) (help, rest string) {
	text = strings.TrimSpace(text)
	if loc := blankLine.FindStringIndex(text); loc != nil {
		return strings.Join(strings.Fields(text[:loc[0]]), " "), strings.TrimSpace(text[loc[0]:])
	}
	return strings.Join(strings.Fields(text), " "), ""
}

var camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)

func kebab(name string) string {
	name = strings.ReplaceAll(name, "_", "-")
	return strings.ToLower(camelBoundary.ReplaceAllString(name, "$1-$2"))
}

// schemaTypes is the set of JSON types a schema accepts, without "null".
func schemaTypes(s *jsonschema.Schema) map[string]bool {
	types := map[string]bool{}
	if s == nil {
		return types
	}
	if s.Type != "" {
		types[s.Type] = true
	}
	for _, t := range s.Types {
		types[t] = true
	}
	for _, branch := range append(slices.Clone(s.AnyOf), s.OneOf...) {
		for t := range schemaTypes(branch) {
			types[t] = true
		}
	}
	for _, v := range s.Enum {
		types[jsonType(v)] = true
	}
	if s.Const != nil {
		types[jsonType(*s.Const)] = true
	}
	delete(types, "null")
	return types
}

func jsonType(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if x == float64(int64(x)) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Pointer, reflect.Interface:
		if reflect.ValueOf(v).IsNil() {
			return "null"
		}
	}
	return "object"
}
