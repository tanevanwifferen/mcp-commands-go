// Package mcpcommands builds MCP servers shaped like a CLI: a command tree
// behind two tools, help and run.
//
//	app := mcpcommands.New(mcpcommands.Options{Name: "beads", Description: "Issue tracker"})
//	issue := app.Group("issue", "Manage issues")
//	mcpcommands.Add(issue, "create", "Create an issue.", func(ctx context.Context, req *mcp.CallToolRequest, in CreateInput) (any, error) {
//		return in, nil
//	})
//	err := app.Run(ctx, &mcp.StdioTransport{})
package mcpcommands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configures a [Server].
type Options struct {
	// Name is the server name, also the title of the root help page.
	Name    string
	Version string
	// Description says what the server is for; the first thing the model reads.
	Description string
	// Instructions are the MCP instructions sent at initialization. They default
	// to the description plus a short explanation of help and run.
	Instructions string
	// HelpTool and RunTool name the two tools. Defaults "help" and "run".
	HelpTool string
	RunTool  string
	// HideCommandsInDescription leaves the top-level command listing out of the
	// help tool's description. By default it is included, so the model knows
	// the commands without a call.
	HideCommandsInDescription bool
	// Implementation and ServerOptions are passed on to mcp.NewServer. Name,
	// Version and Instructions above take precedence.
	Implementation *mcp.Implementation
	ServerOptions  *mcp.ServerOptions
}

// Server is an MCP server whose commands are discovered with help and executed with run.
type Server struct {
	// MCP is the underlying server; add resources and prompts to it as usual.
	MCP *mcp.Server

	root *Group
	opts Options
}

// New creates a Server. Add commands with [Add] and groups with [Server.Group].
func New(opts Options) *Server {
	if opts.HelpTool == "" {
		opts.HelpTool = "help"
	}
	if opts.RunTool == "" {
		opts.RunTool = "run"
	}
	if opts.Version == "" {
		opts.Version = "0.0.0"
	}
	impl := mcp.Implementation{}
	if opts.Implementation != nil {
		impl = *opts.Implementation
	}
	impl.Name, impl.Version = opts.Name, opts.Version
	so := mcp.ServerOptions{}
	if opts.ServerOptions != nil {
		so = *opts.ServerOptions
	}
	so.Instructions = opts.Instructions
	if so.Instructions == "" {
		if opts.Description != "" {
			so.Instructions = opts.Description + "\n\n"
		}
		so.Instructions += fmt.Sprintf("This server is a command tree, used like a CLI. Call %[1]s() to list the commands, "+
			`%[1]s("<command path>") for a group or a command's arguments, `+
			`and %[2]s("<command path> <arguments> --option value") to execute one.`, opts.HelpTool, opts.RunTool)
	}
	root := &Group{Name: opts.Name}
	root.Help, root.Description = splitDescription(opts.Description)
	return &Server{MCP: mcp.NewServer(&impl, &so), root: root, opts: opts}
}

func (s *Server) group() *Group { return s.root }

// Group creates a top-level group, e.g. issue := app.Group("issue", "Manage issues").
func (s *Server) Group(name, description string, aliases ...string) *Group {
	return s.root.Group(name, description, aliases...)
}

// -- Using the tree directly (tests, other front ends) --

// HelpText is the help page for a path: "" for the root, "issue", "issue create", ...
// depth is how many levels of nested groups to list.
func (s *Server) HelpText(path string, depth int) (string, error) {
	node, found, rest := walk(s.root, splitPath(path))
	if len(rest) > 0 {
		return "", unknownCommand(node.(*Group), found, rest[0])
	}
	return render(node, found, depth), nil
}

// Execute parses and runs one command line, returning what the command returned.
// args holds optional parameter values by name.
func (s *Server) Execute(ctx context.Context, line string, args map[string]any) (any, error) {
	return s.execute(ctx, nil, line, args)
}

func (s *Server) execute(ctx context.Context, req *mcp.CallToolRequest, line string, args map[string]any) (any, error) {
	tokens, err := tokenize(line)
	if err != nil {
		return nil, err
	}
	node, path, rest := walk(s.root, tokens)
	options := rest
	if end := slices.Index(rest, "--"); end >= 0 {
		options = rest[:end]
	}
	if slices.Contains(options, "--help") || slices.Contains(options, "-h") {
		return render(node, path, 1), nil
	}
	if g, ok := node.(*Group); ok {
		if len(rest) > 0 {
			return nil, unknownCommand(g, path, rest[0])
		}
		what := "No command given"
		if len(path) > 0 {
			what = fmt.Sprintf("%s is a group, not a command", jsonString(strings.Join(path, " ")))
		}
		return nil, Errorf("%s.\n\n%s", what, renderGroup(g, path, 1))
	}
	c := node.(*Command)
	values, err := bind(c, rest, args)
	if err == nil {
		var out any
		out, err = c.invoke(ctx, req, values)
		if err == nil {
			return out, nil
		}
	}
	var usage *UsageError
	if errors.As(err, &usage) {
		return nil, &UsageError{Msg: usage.Msg + "\n\n" + renderCommand(c, path)}
	}
	return nil, err
}

// -- MCP --

// Run registers the help and run tools for the current tree and serves on t until the client disconnects.
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	s.RegisterTools()
	return s.MCP.Run(ctx, t)
}

// Connect registers the help and run tools and starts a session on t.
func (s *Server) Connect(ctx context.Context, t mcp.Transport) (*mcp.ServerSession, error) {
	s.RegisterTools()
	return s.MCP.Connect(ctx, t, nil)
}

type helpInput struct {
	Path  string `json:"path,omitempty" jsonschema:"Command path, e.g. \"\" for the root (the default), \"issue\", \"issue create\"."`
	Depth int    `json:"depth,omitempty" jsonschema:"How many levels of nested groups to list. Default 1."`
}

type runInput struct {
	Command string         `json:"command" jsonschema:"One command line: path, arguments, options. E.g. issue create \"Title\" --priority 1"`
	Args    map[string]any `json:"args,omitempty" jsonschema:"Optional parameter values by name, for structured input."`
}

// RegisterTools registers (or re-registers) the help and run tools. Run and
// Connect do this for you; call it yourself if you serve s.MCP directly.
func (s *Server) RegisterTools() {
	helpDescription := "List the commands of this server and show how to call them. " +
		`help() lists the top-level commands, help("<group>") a group's commands, ` +
		`help("<group> <command>") a command's arguments and options. ` +
		"Raise depth to list nested groups too."
	if s.root.Help != "" {
		helpDescription = s.root.Name + ": " + s.root.Help + "\n\n" + helpDescription
	}
	if !s.opts.HideCommandsInDescription && len(s.root.children) > 0 {
		page := renderGroup(s.root, nil, 1)
		helpDescription += "\n\n" + page[strings.Index(page, "\n\n")+2:]
	}

	helpSchema, err := jsonschema.For[helpInput](nil)
	if err != nil {
		panic(err)
	}
	// No schema defaults: the SDK panics applying them when a client sends null
	// arguments. An empty path is the root and a zero depth means 1.
	lo, hi := 1.0, 10.0
	depth := helpSchema.Properties["depth"]
	depth.Minimum, depth.Maximum = &lo, &hi

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        s.opts.HelpTool,
		Description: helpDescription,
		InputSchema: helpSchema,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in helpInput) (*mcp.CallToolResult, any, error) {
		page, err := s.HelpText(in.Path, in.Depth)
		if err != nil {
			return text(err.Error(), true), nil, nil
		}
		return text(page, false), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        s.opts.RunTool,
		Description: usage,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in runInput) (*mcp.CallToolResult, any, error) {
		out, err := s.execute(ctx, req, in.Command, in.Args)
		if err != nil {
			return text(err.Error(), true), nil, nil
		}
		return toResult(out), nil, nil
	})
}

func text(value string, isError bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: value}}, IsError: isError}
}

func toResult(value any) *mcp.CallToolResult {
	switch v := value.(type) {
	case *mcp.CallToolResult:
		return v
	case nil:
		return text("OK", false)
	case string:
		return text(v, false)
	}
	b, err := json.Marshal(value)
	if err != nil {
		return text(fmt.Sprintf("cannot encode result: %v", err), true)
	}
	return text(string(b), false)
}
