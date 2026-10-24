# mcp-commands

**MCP servers shaped like a CLI.** You define a tree of commands, like `git` or `beads`, and the
model sees only two tools: `help` and `run`.

```
issue create "Fix login" 1 bd-3 --description "Users get logged out after 5 minutes"
```

A regular MCP server sends the model a schema for every tool. That costs context before the
model has done anything, and the cost grows with each tool you add. mcp-commands keeps the
tool list at two however many commands you have. The model reads help for a command only when
it is about to use it, the way you'd run `--help`.

Also available for Python: [mcp-commands-python](https://github.com/tanevanwifferen/mcp-commands-python)
and TypeScript: [mcp-commands-typescript](https://github.com/tanevanwifferen/mcp-commands-typescript).

## Install

```sh
go get github.com/tanevanwifferen/mcp-commands-go
```

Requires Go 1.25+. Built on the official [`go-sdk`](https://github.com/modelcontextprotocol/go-sdk).

## Example

```go
package main

import (
	"context"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	commands "github.com/tanevanwifferen/mcp-commands-go"
)

type CreateInput struct {
	Title       string   `json:"title" cli:"arg" jsonschema:"Short summary of the work."`
	Priority    int      `json:"priority,omitempty" cli:"arg" default:"2" minimum:"0" maximum:"4" jsonschema:"0 is the most urgent, 4 the least."`
	Deps        []string `json:"deps,omitempty" cli:"arg" jsonschema:"IDs of issues that must be closed first."`
	Description string   `json:"description,omitempty" jsonschema:"Longer explanation, acceptance criteria."`
}

type ListInput struct {
	All bool `json:"all,omitempty"`
}

func main() {
	app := commands.New(commands.Options{
		Name:        "beads",
		Version:     "1.0.0",
		Description: "Issue tracker with dependencies. Use it to plan work and find what is ready next.",
	})

	issue := app.Group("issue", "Create, update and inspect issues")

	commands.Add(issue, "create", "Create an issue.",
		func(ctx context.Context, req *mcp.CallToolRequest, in CreateInput) (any, error) {
			// in is parsed and validated
			return in, nil
		})

	commands.Add(issue, "list", "List issues, open ones by default.",
		func(ctx context.Context, req *mcp.CallToolRequest, in ListInput) (any, error) {
			return []string{}, nil
		}, "ls")

	commands.Add(app, "ready", "Open issues whose dependencies are all closed.",
		func(context.Context, *mcp.CallToolRequest, struct{}) (any, error) {
			return []string{}, nil
		})

	if err := app.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
```

The API follows `mcp.AddTool`:

- A command's input is a struct. Its exported fields are the parameters, named by their `json` tags.
- Fields tagged `cli:"arg"` are **arguments**: positional, in field order. The other fields are
  **options** and can only be passed as `--name value`.
- Fields without `omitempty`/`omitzero`, and without a default, are required.
- `jsonschema:"..."` gives a parameter its description. `default`, `enum` (comma-separated),
  `minimum` and `maximum` tags add those constraints; on a slice they apply to its items.
- If the input type has a `Validate() error` method (on the value or the pointer), it runs after parsing.
- In the description, the first paragraph is the one-liner shown in listings. The rest appears
  on the command's help page.
- `req` is the `*mcp.CallToolRequest`, or `nil` when you call `Execute` directly.
- A handler can return a string, any JSON-serializable value, or `nil` (shown as `OK`).
  It can also return a `*mcp.CallToolResult`.
- A returned error fails the call with exactly its message. `commands.Errorf` makes a
  `*CommandError`; `commands.Usagef` makes a `*UsageError`, which also gets the command's help appended.
- `Add` panics on an invalid definition (bad name, duplicate, non-struct input), like `http.HandleFunc`.

Groups nest as deep as you like: `issue.Group("label", "Manage labels")`.

## What the model sees

**`help(path = "", depth = 1)`**. The top-level listing is already in this tool's
description and in the server's MCP instructions, so the model usually doesn't need to call
it for the root:

```
beads: Issue tracker with dependencies. Use it to plan work and find what is ready next.

Commands:
  issue …  Create, update and inspect issues (4 commands)
  ready    Open issues whose dependencies are all closed.
```

`help("issue create")`:

```
Usage: issue create <title> [priority] [deps...] [options]

Create an issue.

Arguments:
  title     (string, required)  Short summary of the work.
  priority  (integer, default 2, >= 0, <= 4)  0 is the most urgent, 4 the least.
  deps      (string[])  IDs of issues that must be closed first.

Options:
  --description  (string)  Longer explanation, acceptance criteria.
```

**`run(command, args?)`** executes one command line. `args` is an optional object of
parameter values, for input that is awkward to quote.

Both tools are titled and described per server (`beads: run a command`, and a `run`
description that starts with the server's name, description and top-level commands, with an
example taken from its own tree), so a client with several mcp-commands servers connected can
tell their `run` tools apart.

## Command-line rules

- The first words are the command path. Aliases work (`issue ls`).
- Arguments are positional, in declaration order. They can also be given as `--name value`.
- Options take `--name value` or `--name=value`. camelCase and snake_case names become kebab-case (`dryRun` → `--dry-run`).
- Booleans: `--flag` sets true, `--no-flag` sets false.
- Arrays: an array argument in the last position takes the remaining words (`issue create t 1 bd-1 bd-2`).
  Otherwise repeat the option (`--deps a --deps b`) or pass JSON (`--deps '["a","b"]'`).
- Quoting works like a POSIX shell. `--` ends options. `--help` shows the command's help.
- Words for string parameters stay strings, so `123` stays `"123"`. Words for other types are
  parsed as JSON (`3`, `true`, `{"a": 1}`).

Errors are written for the model to act on:

```
--status: expected one of "open"|"in_progress"|"closed" (got string)

Usage: issue update <id> [options]
...
```

```
Unknown command "isue" in the root. Did you mean: issue? Available: issue, ready.
```

## Options

```go
commands.New(commands.Options{
	Name, Version, Description,
	Instructions:              "",    // defaults to the description plus how to use help/run
	HelpTool:                  "help", // tool names
	RunTool:                   "run",
	HideCommandsInDescription: false, // leave the root listing out of the help tool's description
	Implementation:            nil,   // extra *mcp.Implementation fields
	ServerOptions:             nil,   // *mcp.ServerOptions for mcp.NewServer
})
```

`app.MCP` is the underlying `*mcp.Server`, so you can add resources and prompts to it as usual.
`Run` and `Connect` register the two tools. If you serve `app.MCP` yourself (for example with
`mcp.NewStreamableHTTPHandler`), call `app.RegisterTools()` first.

## Testing without MCP

```go
out, err := app.Execute(ctx, `issue create "Fix login" --priority 1`, nil) // what the handler returned
page, err := app.HelpText("issue create", 1)
```

## Development

```sh
go test ./...
go run ./examples/beads
```

## License

MIT
