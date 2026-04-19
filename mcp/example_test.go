package mcp_test

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/v8tix/mcp-toolkit/handler"
	"github.com/v8tix/mcp-toolkit/mcp"
	"github.com/v8tix/mcp-toolkit/model"
	"github.com/v8tix/mcp-toolkit/registry"
)

// stubSession is a no-op mcp.Session for documentation examples.
type exampleSession struct{}

func (exampleSession) CallTool(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}},
	}, nil
}

func ExampleNewTool() {
	discovered := &sdkmcp.Tool{Name: "search_web", Description: "Search the web."}
	tool := mcp.NewTool(discovered, exampleSession{})

	fmt.Println(tool.Definition().Function.Name)
	fmt.Println(tool.Definition().Function.Description)
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

	customDef := model.ToolDefinition{
		Type: "function",
		Function: model.FunctionDefinition{
			Name:        "search_web",
			Description: "Custom description.",
		},
	}

	tool := mcp.NewTool(discovered, exampleSession{}).WithDefinition(customDef)

	fmt.Println(tool.Definition().Function.Description)
	// Output:
	// Custom description.
}

func ExampleBuilder_WithDefinitionFunc() {
	discovered := &sdkmcp.Tool{Name: "search_web", Description: "Search the web."}

	tool := mcp.NewTool(discovered, exampleSession{}).
		WithDefinitionFunc(func(t *sdkmcp.Tool) model.ToolDefinition {
			def := mcp.BuildDefinition(t)
			def.Function.Description = "[cached] " + def.Function.Description
			return def
		})

	fmt.Println(tool.Definition().Function.Description)
	// Output:
	// [cached] Search the web.
}

func ExampleRegisterTools() {
	type greetArgs struct {
		Name string `json:"name" desc:"Name to greet."`
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
