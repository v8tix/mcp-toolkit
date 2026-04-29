package registry_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/v8tix/mcp-toolkit/v2/handler"
	"github.com/v8tix/mcp-toolkit/v2/model"
	"github.com/v8tix/mcp-toolkit/v2/registry"
)

type greetArgs struct {
	Name string `json:"name" description:"Name to greet."`
}

func greetTool(name, desc string) handler.ExecutableTool {
	return handler.NewTool(name, desc,
		func(_ context.Context, in greetArgs) (string, error) {
			return "hello, " + in.Name, nil
		},
	)
}

func ExampleNew() {
	reg := registry.New(
		greetTool("greet", "Greet someone."),
		greetTool("farewell", "Say goodbye."),
	)

	fmt.Println(reg.Names())
	// Output:
	// [greet farewell]
}

func ExampleRegistry_Add() {
	reg := registry.New(greetTool("greet", "Greet someone."))
	reg.Add(greetTool("farewell", "Say goodbye."))

	fmt.Println(reg.Names())
	// Output:
	// [greet farewell]
}

func ExampleRegistry_ByName() {
	reg := registry.New(greetTool("greet", "Greet someone."))

	t, ok := reg.ByName("greet")
	fmt.Println(ok)
	fmt.Println(t.Definition().Name)
	// Output:
	// true
	// greet
}

func ExampleRegistry_Remove() {
	reg := registry.New(
		greetTool("greet", "Greet someone."),
		greetTool("farewell", "Say goodbye."),
	)

	removed := reg.Remove("greet")
	fmt.Println(removed)
	fmt.Println(reg.Names())
	// Output:
	// true
	// [farewell]
}

func ExampleRegistry_Filter() {
	reg := registry.New(
		greetTool("public_greet", "Public."),
		greetTool("internal_sync", "Internal."),
		greetTool("public_farewell", "Public."),
	)

	public := reg.Filter(func(t model.Tool) bool {
		return !strings.HasPrefix(t.Definition().Name, "internal_")
	})

	fmt.Println(public.Names())
	fmt.Println(reg.Names()) // original unchanged
	// Output:
	// [public_greet public_farewell]
	// [public_greet internal_sync public_farewell]
}
