package mcpcommands_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	commands "github.com/tanevanwifferen/mcp-commands-go"
)

type createInput struct {
	Title    string   `json:"title" cli:"arg" jsonschema:"Short summary."`
	Priority int      `json:"priority,omitempty" cli:"arg" default:"2" jsonschema:"0-4."`
	Deps     []string `json:"deps,omitempty" cli:"arg"`
	Draft    bool     `json:"draft,omitempty"`
	DryRun   bool     `json:"dryRun,omitempty"`
}

type idInput struct {
	ID string `json:"id" cli:"arg"`
}

type labelInput struct {
	ID     string   `json:"id" cli:"arg"`
	Labels []string `json:"labels" cli:"arg"`
}

type statusInput struct {
	Status string `json:"status,omitempty" enum:"open,closed"`
	Limit  int    `json:"limit,omitempty" minimum:"1" default:"10"`
}

func makeApp() *commands.Server {
	app := commands.New(commands.Options{Name: "beads", Description: "Issue tracker"})
	issue := app.Group("issue", "Manage issues")

	commands.Add(issue, "create", "Create an issue.", func(_ context.Context, _ *mcp.CallToolRequest, in createInput) (any, error) {
		return in, nil
	})
	commands.Add(issue, "delete", "Delete an issue.", func(_ context.Context, _ *mcp.CallToolRequest, in idInput) (any, error) {
		if in.ID == "bd-0" {
			return nil, commands.Errorf("bd-0 is protected")
		}
		return "deleted " + in.ID, nil
	}, "rm")
	commands.Add(issue, "list", "List issues.", func(_ context.Context, _ *mcp.CallToolRequest, in statusInput) (any, error) {
		return in, nil
	})

	label := issue.Group("label", "Manage labels")
	commands.Add(label, "add", "Add labels.", func(_ context.Context, _ *mcp.CallToolRequest, in labelInput) (any, error) {
		return in.Labels, nil
	})

	commands.Add(app, "ready", "Show ready work.", func(context.Context, *mcp.CallToolRequest, struct{}) (any, error) {
		return nil, nil
	})
	return app
}

func run(t *testing.T, app *commands.Server, line string, args map[string]any) any {
	t.Helper()
	out, err := app.Execute(context.Background(), line, args)
	if err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	return out
}

func fails(t *testing.T, app *commands.Server, line string, args map[string]any, pattern string) error {
	t.Helper()
	_, err := app.Execute(context.Background(), line, args)
	if err == nil {
		t.Fatalf("%s: expected an error", line)
	}
	if !regexp.MustCompile(pattern).MatchString(err.Error()) {
		t.Fatalf("%s: error %q does not match %q", line, err, pattern)
	}
	return err
}

func TestBindsPositionalsAndOptions(t *testing.T) {
	app := makeApp()
	got := run(t, app, `issue create "Fix login" 1 bd-1 bd-2`, nil).(createInput)
	want := createInput{Title: "Fix login", Priority: 1, Deps: []string{"bd-1", "bd-2"}}
	if got.Title != want.Title || got.Priority != 1 || strings.Join(got.Deps, ",") != "bd-1,bd-2" || got.Draft {
		t.Fatalf("got %+v", got)
	}

	got = run(t, app, "issue create x --priority=3 --deps a --deps b --draft --dry-run", nil).(createInput)
	if got.Priority != 3 || strings.Join(got.Deps, ",") != "a,b" || !got.Draft || !got.DryRun {
		t.Fatalf("got %+v", got)
	}

	got = run(t, app, `issue create x --deps '["a","b"]' --no-draft`, nil).(createInput)
	if strings.Join(got.Deps, ",") != "a,b" || got.Draft || got.Priority != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestArgsObject(t *testing.T) {
	app := makeApp()
	got := run(t, app, "issue create", map[string]any{"title": "t", "deps": []any{"a"}, "draft": true}).(createInput)
	if got.Title != "t" || got.Priority != 2 || got.Deps[0] != "a" || !got.Draft {
		t.Fatalf("got %+v", got)
	}
	fails(t, app, "issue create t", map[string]any{"title": "u"}, "both")
}

func TestNumbersStayStringsForStringParams(t *testing.T) {
	if got := run(t, makeApp(), "issue create 123", nil).(createInput); got.Title != "123" {
		t.Fatalf("got %+v", got)
	}
}

func TestNestedGroupsAndTrailingArray(t *testing.T) {
	got := run(t, makeApp(), "issue label add bd-1 a b c", nil).([]string)
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("got %v", got)
	}
}

func TestAliasesAndCommandErrors(t *testing.T) {
	app := makeApp()
	if got := run(t, app, "issue rm bd-3", nil); got != "deleted bd-3" {
		t.Fatalf("got %v", got)
	}
	err := fails(t, app, "issue delete bd-0", nil, "^bd-0 is protected$")
	var ce *commands.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("not a CommandError: %T", err)
	}
}

func TestExplainsErrors(t *testing.T) {
	app := makeApp()
	fails(t, app, "issue craete x", nil, "Did you mean: issue create")
	fails(t, app, "issue create", nil, `missing required argument <title>[\s\S]*Usage: issue create <title>`)
	fails(t, app, "issue create x --prio 1", nil, "Did you mean --priority")
	err := fails(t, app, "issue create x high", nil, "priority")
	var ue *commands.UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("not a UsageError: %T", err)
	}
	fails(t, app, "issue", nil, "is a group")
	fails(t, app, "issue create 'unterminated", nil, "closing quotation")
	fails(t, app, "issue list --status done", nil, `--status: .*\(got "done"\)`)
	fails(t, app, "issue list --limit 0", nil, `--limit: .*\(got 0\)`)
}

func TestHelpFlagInline(t *testing.T) {
	if got := run(t, makeApp(), "issue create --help", nil).(string); !strings.HasPrefix(got, "Usage: issue create") {
		t.Fatalf("got %q", got)
	}
}

func TestHelpPages(t *testing.T) {
	app := makeApp()
	help := func(path string, depth int) string {
		page, err := app.HelpText(path, depth)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	root := help("", 1)
	for _, want := range []string{"issue …", "(4 commands)"} {
		if !strings.Contains(root, want) {
			t.Fatalf("root help lacks %q:\n%s", want, root)
		}
	}
	if strings.Contains(root, "label") {
		t.Fatalf("root help lists nested group:\n%s", root)
	}
	if !strings.Contains(help("", 2), "  label …") {
		t.Fatal("depth 2 does not list nested group")
	}
	cmd := help("issue create", 1)
	for _, want := range []string{
		"Usage: issue create <title> [priority] [deps...] [options]",
		"Short summary.",
		"(integer, default 2)",
		"--dry-run",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("command help lacks %q:\n%s", want, cmd)
		}
	}
	if !strings.Contains(help("issue list", 1), "(open|closed)") {
		t.Fatal("enum not shown")
	}
	if help("issue/label", 1) != help("issue > label", 1) {
		t.Fatal("path separators differ")
	}
}

func TestOverMCP(t *testing.T) {
	ctx := context.Background()
	app := makeApp()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := app.Connect(ctx, serverTransport); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "help,run" {
		t.Fatalf("tools: %v", names)
	}
	if !strings.Contains(tools.Tools[0].Description, "issue …") {
		t.Fatalf("help description lacks listing: %s", tools.Tools[0].Description)
	}

	call := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return r.Content[0].(*mcp.TextContent).Text, r.IsError
	}

	if out, isErr := call("help", map[string]any{"path": "issue"}); isErr || !strings.Contains(out, "create <title>") {
		t.Fatalf("help: %v %s", isErr, out)
	}
	if out, isErr := call("help", nil); isErr || !strings.Contains(out, "ready") {
		t.Fatalf("help root: %v %s", isErr, out)
	}
	out, isErr := call("run", map[string]any{"command": "issue create 'A b' --priority 0"})
	if isErr || out != `{"title":"A b"}` {
		t.Fatalf("run: %v %s", isErr, out)
	}
	if out, isErr := call("run", map[string]any{"command": "issue delete bd-0"}); !isErr || out != "bd-0 is protected" {
		t.Fatalf("run error: %v %s", isErr, out)
	}
	if out, _ := call("run", map[string]any{"command": "ready"}); out != "OK" {
		t.Fatalf("ready: %s", out)
	}
}
