// A toy issue tracker in the style of beads, served as a command tree.
//
// Run it with: go run ./examples/beads
package main

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	commands "github.com/tanevanwifferen/mcp-commands-go"
)

type Issue struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Priority    int      `json:"priority"`
	Deps        []string `json:"deps"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
}

var (
	mu     sync.Mutex
	issues = map[string]*Issue{}
	order  []string
)

func find(id string) (*Issue, error) {
	if found, ok := issues[id]; ok {
		return found, nil
	}
	return nil, commands.Errorf("no issue %q", id)
}

type CreateInput struct {
	Title       string   `json:"title" cli:"arg" jsonschema:"Short summary of the work."`
	Priority    int      `json:"priority,omitempty" cli:"arg" default:"2" minimum:"0" maximum:"4" jsonschema:"0 is the most urgent, 4 the least."`
	Deps        []string `json:"deps,omitempty" cli:"arg" jsonschema:"IDs of issues that must be closed first."`
	Description string   `json:"description,omitempty" jsonschema:"Longer explanation, acceptance criteria."`
}

type ShowInput struct {
	ID string `json:"id" cli:"arg" jsonschema:"Issue ID, e.g. bd-1."`
}

type UpdateInput struct {
	ID       string `json:"id" cli:"arg"`
	Status   string `json:"status,omitempty" enum:"open,in_progress,closed"`
	Priority *int   `json:"priority,omitempty"`
}

type ListInput struct {
	Status string `json:"status,omitempty" enum:"open,in_progress,closed" jsonschema:"Only issues with this status."`
	All    bool   `json:"all,omitempty" jsonschema:"Include closed issues."`
}

func newApp() *commands.Server {
	app := commands.New(commands.Options{
		Name:        "beads",
		Version:     "0.1.0",
		Description: "Issue tracker with dependencies. Use it to plan work and find what is ready to do next.",
	})

	issue := app.Group("issue", "Create, update and inspect issues")

	commands.Add(issue, "create", "Create an issue.", func(_ context.Context, _ *mcp.CallToolRequest, in CreateInput) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		for _, dep := range in.Deps {
			if _, err := find(dep); err != nil {
				return nil, err
			}
		}
		id := fmt.Sprintf("bd-%d", len(issues)+1)
		if in.Deps == nil {
			in.Deps = []string{}
		}
		created := &Issue{ID: id, Title: in.Title, Priority: in.Priority, Deps: in.Deps, Description: in.Description, Status: "open"}
		issues[id] = created
		order = append(order, id)
		return created, nil
	})

	commands.Add(issue, "show", "Show one issue.", func(_ context.Context, _ *mcp.CallToolRequest, in ShowInput) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		return find(in.ID)
	}, "get")

	commands.Add(issue, "update", "Change the status or priority of an issue.", func(_ context.Context, _ *mcp.CallToolRequest, in UpdateInput) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		found, err := find(in.ID)
		if err != nil {
			return nil, err
		}
		if in.Status != "" {
			found.Status = in.Status
		}
		if in.Priority != nil {
			found.Priority = *in.Priority
		}
		return found, nil
	})

	commands.Add(issue, "list", "List issues, open ones by default.", func(_ context.Context, _ *mcp.CallToolRequest, in ListInput) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		out := []*Issue{}
		for _, id := range order {
			i := issues[id]
			if (in.Status == "" || i.Status == in.Status) && (in.All || i.Status != "closed") {
				out = append(out, i)
			}
		}
		return out, nil
	}, "ls")

	commands.Add(app, "ready", "Open issues whose dependencies are all closed, most urgent first.", func(context.Context, *mcp.CallToolRequest, struct{}) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		out := []*Issue{}
		for _, id := range order {
			i := issues[id]
			if i.Status == "open" && !slices.ContainsFunc(i.Deps, func(d string) bool { return issues[d].Status != "closed" }) {
				out = append(out, i)
			}
		}
		slices.SortStableFunc(out, func(a, b *Issue) int { return a.Priority - b.Priority })
		return out, nil
	})
	return app
}

func main() {
	if err := newApp().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
