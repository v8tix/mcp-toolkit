package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/v8tix/mcp-toolkit/handler"
	"github.com/v8tix/mcp-toolkit/model"
	"github.com/v8tix/mcp-toolkit/schema"
)

type searchArgs struct {
	Query string `json:"query" desc:"Search query."`
	Limit *int   `json:"limit,omitempty" desc:"Max results."`
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
	fmt.Println(def.Function.Name)
	fmt.Println(def.Function.Parameters.Properties["query"].Description)
	// Output:
	// search_web
	// Search query.
}

func ExampleNewToolWithDefinition() {
	params := schema.NewInputSchemaFromStruct(searchArgs{})
	def := schema.FormatToolDefinition("search_web", "Search the web.", params, false)

	tool := handler.NewToolWithDefinition(def,
		func(_ context.Context, in searchArgs) ([]searchResult, error) {
			return []searchResult{{URL: "https://example.com", Title: in.Query}}, nil
		},
	)

	fmt.Println(tool.Definition().Function.Name)
	fmt.Println(tool.Definition().Function.Strict)
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

	fmt.Println(logged.Definition().Function.Name)
	// Output:
	// search_web
}

func ExampleExecutableTool_Execute() {
	tool := handler.NewTool("greet", "Greet by name.",
		func(_ context.Context, in struct {
			Name string `json:"name" desc:"Name to greet."`
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

// Compile-time check that *model.ToolDefinition fields are accessible.
var _ model.ToolDefinition = handler.NewTool("t", "d", func(_ context.Context, _ struct{}) (struct{}, error) { return struct{}{}, nil }).Definition()
