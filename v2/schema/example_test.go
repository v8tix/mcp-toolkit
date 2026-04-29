package schema_test

import (
	"fmt"

	"github.com/v8tix/mcp-toolkit/v2/schema"
)

type queryArgs struct {
	Query string `json:"query" description:"Search query."`
	Topic string `json:"topic,omitempty" description:"Category." enum:"general,news,finance"`
	Limit *int   `json:"limit,omitempty" description:"Max results."`
}

func ExampleInputSchema() {
	s := schema.InputSchema(queryArgs{})

	fmt.Println(s["type"])
	props := s["properties"].(map[string]any)
	fmt.Println(props["query"].(map[string]any)["description"])
	fmt.Println(props["topic"].(map[string]any)["enum"])
	fmt.Println(s["required"])
	// Output:
	// object
	// Search query.
	// [general news finance]
	// [query]
}

func ExampleStrictTool() {
	def := schema.StrictTool("search_web", "Search the web.", queryArgs{})

	fmt.Println(def.Name)
	fmt.Println(def.Description)
	s := def.InputSchema.(map[string]any)
	fmt.Println(s["additionalProperties"])
	// Output:
	// search_web
	// Search the web.
	// false
}

func ExampleTool() {
	// Use Tool for APIs that do not require additionalProperties constraints.
	def := schema.Tool("search_web", "Search the web.", queryArgs{})

	fmt.Println(def.Name)
	s := def.InputSchema.(map[string]any)
	_, hasAdditional := s["additionalProperties"]
	fmt.Println(hasAdditional)
	// Output:
	// search_web
	// false
}
