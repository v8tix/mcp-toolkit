package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/v8tix/mcp-toolkit/v2/handler"
	"github.com/v8tix/mcp-toolkit/v2/schema"
)

type searchArgs struct {
	Query string `json:"query" description:"Search query."`
	Limit *int   `json:"limit,omitempty" description:"Max results."`
}

type searchResult struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

func ExampleNewTool() {
	tool := handler.NewTool("search_web", "Search the web.",
		func(_ context.Context, in searchArgs) ([]searchResult, error) {
			return []searchResult{{URL: "https://example.com", Title: in.Query}}, nil
		},
	)

	def := tool.Definition()
	fmt.Println(def.Name)
	props := def.InputSchema.(map[string]any)["properties"].(map[string]any)
	fmt.Println(props["query"].(map[string]any)["description"])
	// Output:
	// search_web
	// Search query.
}

func ExampleNewToolWithDefinition() {
	def := schema.Tool("search_web", "Search the web.", searchArgs{})

	tool := handler.NewToolWithDefinition(def,
		func(_ context.Context, in searchArgs) ([]searchResult, error) {
			return []searchResult{{URL: "https://example.com", Title: in.Query}}, nil
		},
	)

	fmt.Println(tool.Definition().Name)
	s := tool.Definition().InputSchema.(map[string]any)
	_, hasAdditional := s["additionalProperties"]
	fmt.Println(hasAdditional)
	// Output:
	// search_web
	// false
}

func ExampleWrap() {
	base := handler.NewTool("search_web", "Search the web.",
		func(_ context.Context, in searchArgs) ([]searchResult, error) {
			return nil, nil
		},
	)

	logged := handler.Wrap(base, func(ctx context.Context, rawArgs json.RawMessage, next handler.ExecuteFunc) (any, error) {
		log.Printf("calling search_web with %s", rawArgs)
		return next(ctx, rawArgs)
	})

	fmt.Println(logged.Definition().Name)
	// Output:
	// search_web
}

func ExampleExecutableTool_Execute() {
	tool := handler.NewTool("greet", "Greet by name.",
		func(_ context.Context, in struct {
			Name string `json:"name" description:"Name to greet."`
		}) (string, error) {
			return "hello, " + in.Name, nil
		},
	)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"world"}`))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result)
	// Output:
	// hello, world
}

func ExampleErrInvalidArguments() {
	tool := handler.NewTool("greet", "Greet.",
		func(_ context.Context, in struct {
			Name string `json:"name"`
		}) (string, error) {
			return in.Name, nil
		},
	)

	_, err := tool.Execute(context.Background(), json.RawMessage(`not-json`))
	fmt.Println(errors.Is(err, handler.ErrInvalidArguments))
	// Output:
	// true
}

// Compile-time check that Definition() returns *sdkmcp.Tool.
var _ *sdkmcp.Tool = handler.NewTool("t", "d", func(_ context.Context, _ struct{}) (struct{}, error) { return struct{}{}, nil }).Definition()
