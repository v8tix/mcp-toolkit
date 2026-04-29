package mcp_test

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/v8tix/mcp-toolkit/v2/handler"
	"github.com/v8tix/mcp-toolkit/v2/mcp"
	"github.com/v8tix/mcp-toolkit/v2/registry"
)

// exampleSession is a no-op mcp.Session for documentation examples.
type exampleSession struct{}

func (exampleSession) CallTool(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}},
	}, nil
}

func ExampleNewTool() {
	discovered := &sdkmcp.Tool{Name: "search_web", Description: "Search the web."}
	tool := mcp.NewTool(discovered, exampleSession{})

	fmt.Println(tool.Definition().Name)
	fmt.Println(tool.Definition().Description)
	// Output:
	// search_web
	// Search the web.
}

func ExampleNewTools() {
	discovered := []*sdkmcp.Tool{
		{Name: "search_web", Description: "Search."},
		{Name: "translate", Description: "Translate."},
	}

	tools := mcp.NewTools(discovered, exampleSession{}).WithMaxRetries(0).Build()
	reg := registry.New(tools...)

	fmt.Println(reg.Names())
	// Output:
	// [search_web translate]
}

func ExampleBuilder_WithDefinition() {
	discovered := &sdkmcp.Tool{Name: "search_web", Description: "Original description."}

	customDef := &sdkmcp.Tool{
		Name:        "search_web",
		Description: "Custom description.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}

	tool := mcp.NewTool(discovered, exampleSession{}).WithDefinition(customDef)

	fmt.Println(tool.Definition().Description)
	// Output:
	// Custom description.
}

func ExampleBuilder_WithDefinitionFunc() {
	discovered := &sdkmcp.Tool{Name: "search_web", Description: "Search the web."}

	tool := mcp.NewTool(discovered, exampleSession{}).
		WithDefinitionFunc(func(t *sdkmcp.Tool) *sdkmcp.Tool {
			def := mcp.BuildDefinition(t)
			cp := *def
			cp.Description = "[cached] " + def.Description
			return &cp
		})

	fmt.Println(tool.Definition().Description)
	// Output:
	// [cached] Search the web.
}

func ExampleRegisterTools() {
	type greetArgs struct {
		Name string `json:"name" description:"Name to greet."`
	}

	reg := registry.New(
		handler.NewTool("greet", "Greet someone.", func(_ context.Context, in greetArgs) (string, error) {
			return "hello, " + in.Name, nil
		}),
	)

	s := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "example", Version: "v1"}, nil)
	mcp.RegisterTools(s, reg)

	// Server is ready — tools from reg are registered on s.
	fmt.Println("registered")
	// Output:
	// registered
}
