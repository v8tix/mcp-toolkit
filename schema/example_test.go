package schema_test

import (
	"fmt"

	"github.com/v8tix/mcp-toolkit/schema"
)

type queryArgs struct {
	Query string `json:"query" desc:"Search query."`
	Topic string `json:"topic,omitempty" desc:"Category." enum:"general,news,finance"`
	Limit *int   `json:"limit,omitempty" desc:"Max results."`
}

func ExampleNewInputSchemaFromStruct() {
	s := schema.NewInputSchemaFromStruct(queryArgs{})

	fmt.Println(s.Type)
	fmt.Println(s.Properties["query"].Description)
	fmt.Println(s.Properties["topic"].Enum)
	fmt.Println(s.Required)
	// Output:
	// object
	// Search query.
	// [general news finance]
	// [query]
}

func ExampleNewStrictTool() {
	def := schema.NewStrictTool("search_web", "Search the web.", queryArgs{})

	fmt.Println(def.Type)
	fmt.Println(def.Function.Name)
	fmt.Println(def.Function.Strict)
	// Output:
	// function
	// search_web
	// true
}

func ExampleFormatToolDefinition() {
	params := schema.NewInputSchemaFromStruct(queryArgs{})

	// strict=false for schemas that cannot satisfy OpenAI strict mode requirements.
	def := schema.FormatToolDefinition("search_web", "Search the web.", params, false)

	fmt.Println(def.Function.Name)
	fmt.Println(def.Function.Strict)
	// Output:
	// search_web
	// false
}
