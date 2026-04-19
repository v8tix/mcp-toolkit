# mcp-toolkit

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8.svg)](https://go.dev)

A Go library for building [OpenAI-compatible function-calling](https://platform.openai.com/docs/guides/function-calling) tools with typed handlers, retry-aware execution, and first-class [Model Context Protocol (MCP)](https://modelcontextprotocol.io) support — both as a client and as a server.

The struct is the single source of truth for tool schemas. Adding or removing a field automatically updates the JSON Schema sent to the LLM with no manual synchronisation.

## Install

```bash
go get github.com/v8tix/mcp-toolkit
```

Requires Go 1.26+.

---

## At a Glance

```go
// 1. Define args
type SearchArgs struct {
    Query string `json:"query" desc:"The search query."`
    Limit *int   `json:"limit,omitempty" desc:"Maximum results."`
}

// 2. Create a typed tool
tool := handler.NewTool("search_web", "Search the web.", func(ctx context.Context, in SearchArgs) ([]Result, error) {
    return repo.Search(ctx, in.Query)
})

// 3. Register and send to the LLM
reg := registry.New(tool)
req.Tools = reg.All()

// 4. Dispatch after the LLM responds
t, _ := reg.ByName(call.Function.Name)
result, err := t.(handler.ExecutableTool).Execute(ctx, call.Function.Arguments)
```

---

## Table of Contents

- [Packages](#packages)
- [model](#model)
- [schema](#schema)
- [handler](#handler)
- [registry](#registry)
- [observable](#observable)
- [mcp](#mcp)
  - [Client — consume an MCP server's tools](#client--consume-an-mcp-servers-tools)
  - [Server — expose a registry as an MCP server](#server--expose-a-registry-as-an-mcp-server)
- [Full example — typed tools + registry + MCP server](#full-example--typed-tools--registry--mcp-server)
- [Full example — consume MCP tools + retry + registry](#full-example--consume-mcp-tools--retry--registry)
- [Test coverage](#test-coverage)
- [License](#license)

---

## Packages

| Package | Responsibility |
|---|---|
| [`model`](#model) | Core types: `ToolDefinition`, `InputSchema`, `Tool` interface |
| [`schema`](#schema) | Reflection-based schema builder from struct tags |
| [`handler`](#handler) | Typed execution wrapper, middleware decorator |
| [`registry`](#registry) | Thread-safe, ordered tool catalogue |
| [`observable`](#observable) | Retry-aware execution with exponential backoff |
| [`mcp`](#mcp) | MCP bridge — consume server tools as `ExecutableTool`; expose a registry as an MCP server |

---

## model

Core data types shared by all packages. Import directly only when defining custom `Tool` implementations.

```go
import "github.com/v8tix/mcp-toolkit/model"
```

| Type | Description |
|---|---|
| `ToolDefinition` | Top-level OpenAI tool object (`type`, `function`) |
| `FunctionDefinition` | Tool name, description, parameters schema, strict flag |
| `InputSchema` | JSON Schema object for tool parameters |
| `PropertySchema` | Per-property schema: type, description, enum |
| `Tool` | Interface requiring `Definition() ToolDefinition` |

**`InputSchema.ToMap()`** converts the schema to `map[string]any` — used when registering tools on an MCP server.

---

## schema

Derives `InputSchema` and `ToolDefinition` from Go struct tags at construction time.

```go
import "github.com/v8tix/mcp-toolkit/schema"
```

### Struct tags

| Tag | Purpose | Example |
|---|---|---|
| `json:"name"` | Property name sent to the LLM — **required** | `json:"query"` |
| `json:"name,omitempty"` | Marks the field optional (not in `required`) | `json:"topic,omitempty"` |
| `desc:"…"` | Description shown to the model | `desc:"The search query."` |
| `enum:"a,b,c"` | Restricts a string field to an allowed set | `enum:"general,news,finance"` |

### Required vs optional

| Declaration | Schema effect |
|---|---|
| `Field string \`json:"name"\`` | **required** |
| `Field string \`json:"name,omitempty"\`` | optional |
| `Field *string \`json:"name,omitempty"\`` | optional, can be absent |

### Go → JSON Schema type mapping

| Go type | JSON Schema type |
|---|---|
| `string` | `"string"` |
| `int`, `int8` … `int64` | `"integer"` |
| `float32`, `float64` | `"number"` |
| `bool` | `"boolean"` |
| `[]T` | `"array"` |
| `map[K]V`, `struct` | `"object"` |

### Builder functions

```go
// Derive schema from struct tags — panics if v is not a struct.
schema.NewInputSchemaFromStruct(v any) model.InputSchema

// Build a full ToolDefinition with explicit strict control.
schema.FormatToolDefinition(name, description string, params model.InputSchema, strict bool) model.ToolDefinition

// Convenience: derives schema and always enables strict mode.
schema.NewStrictTool(name, description string, args any) model.ToolDefinition
```

`strict: false` disables OpenAI structured-outputs enforcement. Use it when your schema has fields that cannot satisfy strict mode requirements (e.g. `anyOf`, `oneOf`, or additional properties).

### Example — schema only (no handler)

```go
type SearchArgs struct {
    Query string `json:"query" desc:"Search query."`
    Topic string `json:"topic,omitempty" desc:"Category." enum:"general,news,finance"`
}

// Derive and send directly to the LLM request.
def := schema.NewStrictTool("search_web", "Search the web.", SearchArgs{})
req.Tools = []model.ToolDefinition{def}
```

---

## handler

Creates typed, executable tools from Go functions. The JSON Schema is derived once at construction time from the `In` struct.

```go
import "github.com/v8tix/mcp-toolkit/handler"
```

### Creating tools

```go
// Derive schema from In struct tags (always strict).
tool := handler.NewTool("search_web", "Search the web.",
    func(ctx context.Context, in SearchArgs) ([]Result, error) {
        return repo.Search(ctx, in.Query)
    },
)

// Use a pre-built definition — for non-strict mode, custom schemas, or
// definitions that cannot be expressed via struct tags alone.
def := schema.FormatToolDefinition("search_web", "Search.", params, false)
tool := handler.NewToolWithDefinition(def,
    func(ctx context.Context, in SearchArgs) ([]Result, error) {
        return repo.Search(ctx, in.Query)
    },
)
```

### Interfaces

| Interface | Methods | Used by |
|---|---|---|
| `model.Tool` | `Definition() ToolDefinition` | `registry`, schema only |
| `handler.ExecutableTool` | `Definition()` + `Execute(ctx, rawArgs)` | dispatch loop, MCP bridge |

### Middleware

Wrap a tool to add cross-cutting behaviour without modifying the handler:

```go
type ToolMiddleware func(ctx context.Context, rawArgs json.RawMessage, next ExecuteFunc) (any, error)

tool = handler.Wrap(tool, func(ctx context.Context, rawArgs json.RawMessage, next handler.ExecuteFunc) (any, error) {
    log.Printf("call: %s", rawArgs)
    result, err := next(ctx, rawArgs)
    if err != nil {
        log.Printf("error: %v", err)
    }
    return result, err
})
```

Decorators stack outermost-first:

```go
tool = handler.Wrap(tool, withTimeout(5*time.Second)) // runs second
tool = handler.Wrap(tool, withLogging(log.Printf))    // runs first
```

### Errors

`handler.ErrInvalidArguments` is returned by `Execute` when the raw JSON arguments cannot be unmarshalled into the handler's input type. The `observable` layer treats this as a permanent error and never retries it.

```go
result, err := t.(handler.ExecutableTool).Execute(ctx, call.Function.Arguments)
if errors.Is(err, handler.ErrInvalidArguments) {
    // bad args from the LLM — log and skip, do not retry
}
```

---

## registry

Thread-safe, ordered catalogue of `model.Tool` objects. Decoupled from execution — callers type-assert to `handler.ExecutableTool` at dispatch time.

```go
import "github.com/v8tix/mcp-toolkit/registry"
```

### Creating and populating

```go
reg := registry.New(searchTool, translateTool)
reg.Add(anotherTool).Add(yetAnotherTool) // fluent chaining
```

### Reading

```go
// All definitions in insertion order — pass to LLM request.
req.Tools = reg.All()

// Look up by name, then execute.
t, ok := reg.ByName(call.Function.Name)
result, err := t.(handler.ExecutableTool).Execute(ctx, call.Function.Arguments)

// Ordered name list.
fmt.Println(reg.Names()) // ["search_web", "translate", ...]
```

### Mutating

```go
// Remove a tool by name. Returns true if it existed.
removed := reg.Remove("search_web")

// Create a sub-registry by predicate — original is unchanged.
execOnly := reg.Filter(func(t model.Tool) bool {
    _, ok := t.(handler.ExecutableTool)
    return ok
})

publicTools := reg.Filter(func(t model.Tool) bool {
    return !strings.HasPrefix(t.Definition().Function.Name, "internal_")
})
```

### Key properties

| Property | Detail |
|---|---|
| **Thread-safe** | `All`, `ByName`, `Names`, `Filter` hold a read lock; `Add`, `Remove` hold a write lock |
| **Ordered** | All reads return tools in insertion order |
| **Deduplication** | Re-registering a name replaces in-place, preserving order |
| **Decoupled** | Accepts any `model.Tool`; execution via type assertion |

---

## observable

Wraps any `ExecutableTool` in a retry-aware reactive layer using [rxgo](https://github.com/reactivex/rxgo) and [cenkalti/backoff](https://github.com/cenkalti/backoff).

```go
import "github.com/v8tix/mcp-toolkit/observable"
```

Retry lives in `ExecuteRx`. The synchronous `Execute` path skips retry — use `ExecuteRx` when you need backoff behaviour.

### Creating observable tools

```go
// Primary entry point — typed handler, options applied at construction.
// Go infers [In, Out] from the function signature.
tool := observable.New("search_web", "Search.", myFn)
tool := observable.New("search_web", "Search.", myFn, observable.WithMaxRetries(5))

// Wrap an existing ExecutableTool.
tool := observable.Wrap(handlerTool, observable.WithMaxRetries(3))
```

### Options

| Option | Default | Description |
|---|---|---|
| `WithMaxRetries(n uint64)` | `3` | Max retry attempts after first failure. `0` = no retry |
| `WithClassifier(fn ErrorClassifier)` | treat all as transient | Classify errors; return `observable.Permanent(err)` to stop retrying |
| `WithRetryPolicy(p RetryPolicy)` | exponential backoff | Replace entire retry strategy (circuit breaker, fixed interval, …) |
| `WithErrorPolicy(p ErrorPolicy)` | passthrough | Replace entire error classification strategy |
| `WithOnRetry(fn func(attempt uint64, err error))` | nil | Hook called on each transient failure before next attempt |

```go
tool := observable.New("search_web", "Search.", myFn,
    observable.WithMaxRetries(5),
    observable.WithClassifier(func(err error) error {
        if errors.Is(err, ErrNotFound) {
            return observable.Permanent(err) // stop retrying immediately
        }
        return err
    }),
    observable.WithOnRetry(func(attempt uint64, err error) {
        log.Printf("retry %d after: %v", attempt, err)
    }),
)
```

### Permanent errors

Use `observable.Permanent(err)` anywhere to mark an error as non-retryable:

```go
// In a handler:
if in.Query == "" {
    return nil, observable.Permanent(ErrEmptyQuery)
}

// In a classifier:
observable.WithClassifier(func(err error) error {
    if errors.Is(err, ErrNotFound) { return observable.Permanent(err) }
    return err
})
```

`errors.Is` / `errors.As` still work through the wrapper — `backoff.PermanentError` implements `Unwrap`.

### Custom retry policy

Implement `RetryPolicy` to plug in a circuit breaker or fixed interval:

```go
type RetryPolicy interface {
    MaxRetries() uint64
    NewBackOff() backoff.BackOff
}

type fixedRetryPolicy struct{}
func (fixedRetryPolicy) MaxRetries() uint64         { return 3 }
func (fixedRetryPolicy) NewBackOff() backoff.BackOff { return backoff.NewConstantBackOff(500 * time.Millisecond) }

tool := observable.New("t", "d", myFn, observable.WithRetryPolicy(fixedRetryPolicy{}))
```

### Custom error policy

Implement `ErrorPolicy` for full control over classification logic:

```go
type ErrorPolicy interface {
    Classify(err error) error
}
```

`WithClassifier` is a convenience wrapper for the common case where a single function is enough. `WithErrorPolicy` accepts any `ErrorPolicy` implementation for stateful classifiers (e.g. one that tracks error counts to trip a circuit breaker).

### Executing the observable

```go
var result any
var execErr error
for item := range tool.ExecuteRx(ctx, rawArgs).Observe() {
    if item.E != nil {
        execErr = item.E
    } else {
        result = item.V
    }
}
```

---

## mcp

Bridges the [Model Context Protocol Go SDK](https://github.com/modelcontextprotocol/go-sdk) with mcp-toolkit in both directions.

```go
import "github.com/v8tix/mcp-toolkit/mcp"
```

---

### Client — consume an MCP server's tools

Wrap tools discovered from an MCP server as retry-aware `observable.Tool` objects and drop them into a registry:

```go
session, _ := mcpClient.Connect(ctx, transport, nil)
toolsResult, _ := session.ListTools(ctx, nil)

// Zero options — production defaults (3 retries, exponential backoff).
reg := registry.New(mcp.NewTools(toolsResult.Tools, session).Build()...)
```

#### `NewTool` — single tool builder

```go
tool := mcp.NewTool(discovered, session)
```

#### `NewTools` — batch builder

```go
tools := mcp.NewTools(discovered, session).WithMaxRetries(0).Build()
reg := registry.New(tools...)
```

#### Builder options

All builder methods return a new builder (immutable chaining). `sync.Once` lazy-resolves the final `observable.Tool` on first use.

| Method | Available on | Description |
|---|---|---|
| `WithMaxRetries(n uint64)` | `Builder`, `ToolsBuilder` | Cap retry attempts |
| `WithClassifier(fn)` | `Builder`, `ToolsBuilder` | Custom error classifier |
| `With(opts ...Option)` | `Builder`, `ToolsBuilder` | Pass arbitrary `observable.Option` values |
| `WithDefinition(def model.ToolDefinition)` | `Builder`, `ToolsBuilder` | Override definition with static value |
| `WithDefinitionFunc(fn func(*sdkmcp.Tool) model.ToolDefinition)` | `Builder`, `ToolsBuilder` | Override definition using a function that receives the original `*sdkmcp.Tool` |

```go
// Override description for all tools (e.g. add a prefix for caching).
tools := mcp.NewTools(discovered, session).
    WithDefinitionFunc(func(t *sdkmcp.Tool) model.ToolDefinition {
        def := mcp.BuildDefinition(t) // use your own helper or the default
        def.Function.Description = "[cached] " + def.Function.Description
        return def
    }).
    Build()

// Single tool — custom retry + classifier.
tool := mcp.NewTool(discovered[0], session).
    WithMaxRetries(5).
    WithClassifier(func(err error) error {
        if errors.Is(err, ErrRateLimit) { return observable.Permanent(err) }
        return err
    })

// WithOnRetry is an observable.Option — pass it via With.
tools := mcp.NewTools(discovered, session).
    WithMaxRetries(3).
    With(observable.WithOnRetry(func(attempt uint64, err error) {
        log.Printf("retry %d: %v", attempt, err)
    })).
    Build()
```

#### `Session` interface

Any type with a `CallTool` method satisfies `mcp.Session`. `*sdkmcp.ClientSession` satisfies it out of the box. Implement the interface to add caching, logging, or request transformation:

```go
type Session interface {
    CallTool(ctx context.Context, params *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error)
}

// Example: logging wrapper
type loggingSession struct {
    inner mcp.Session
    log   *log.Logger
}

func (s *loggingSession) CallTool(ctx context.Context, params *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
    s.log.Printf("calling tool %q", params.Name)
    res, err := s.inner.CallTool(ctx, params)
    if err != nil {
        s.log.Printf("tool %q error: %v", params.Name, err)
    }
    return res, err
}
```

---

### Server — expose a registry as an MCP server

Register every tool in a `registry.Registry` on an `*sdkmcp.Server` with a single call:

```go
s := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "my-agent", Version: "v1.0.0"}, nil)
mcp.RegisterTools(s, reg)

http.Handle("/mcp", sdkmcp.NewStreamableHTTPHandler(
    func(_ *http.Request) *sdkmcp.Server { return s },
    &sdkmcp.StreamableHTTPOptions{Stateless: true},
))
```

#### Selective exposure — filter tools

Pass one or more filter functions to expose only a subset of the registry:

```go
// Expose only tools whose names don't start with "internal_".
mcp.RegisterTools(s, reg, func(t model.Tool) bool {
    return !strings.HasPrefix(t.Definition().Function.Name, "internal_")
})

// Multiple filters — tool must pass all of them.
mcp.RegisterTools(s, reg, isPublic, isStable)
```

#### Key properties

| Property | Detail |
|---|---|
| **Error mapping** | Execution errors become tool errors (`IsError=true`), not protocol errors — the LLM can read and react to them |
| **Result encoding** | `Execute` return values are JSON-marshalled into a single `TextContent` item |
| **Non-executable skipped** | Tools that don't implement `handler.ExecutableTool` are silently ignored |
| **Filters** | Variadic — all filters must return `true` for a tool to be registered |

---

## Full example — typed tools + registry + MCP server

```go
package main

import (
    "context"
    "net/http"

    sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
    "github.com/v8tix/mcp-toolkit/handler"
    "github.com/v8tix/mcp-toolkit/mcp"
    "github.com/v8tix/mcp-toolkit/registry"
)

type SearchArgs struct {
    Query string `json:"query" desc:"The search query."`
    Limit *int   `json:"limit,omitempty" desc:"Maximum results."`
}

type SearchResult struct {
    URL   string `json:"url"`
    Title string `json:"title"`
}

type TranslateArgs struct {
    Text       string `json:"text" desc:"Text to translate."`
    TargetLang string `json:"target_lang" desc:"Target language code." enum:"en,es,fr,de"`
}

func main() {
    reg := registry.New(
        handler.NewTool("search_web", "Search the web.", func(ctx context.Context, in SearchArgs) ([]SearchResult, error) {
            return searchService.Search(ctx, in.Query)
        }),
        handler.NewTool("translate", "Translate text.", func(ctx context.Context, in TranslateArgs) (string, error) {
            return translateService.Translate(ctx, in.Text, in.TargetLang)
        }),
    )

    s := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "my-agent", Version: "v1.0.0"}, nil)
    mcp.RegisterTools(s, reg)

    http.Handle("/mcp", sdkmcp.NewStreamableHTTPHandler(
        func(_ *http.Request) *sdkmcp.Server { return s },
        &sdkmcp.StreamableHTTPOptions{Stateless: true},
    ))
    http.ListenAndServe(":8080", nil)
}
```

---

## Full example — consume MCP tools + retry + registry

```go
package main

import (
    "context"
    "log"

    sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
    "github.com/v8tix/mcp-toolkit/mcp"
    "github.com/v8tix/mcp-toolkit/observable"
    "github.com/v8tix/mcp-toolkit/registry"
)

func main() {
    client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "my-client", Version: "v1"}, nil)

    // transport is your stdio/HTTP/SSE transport (see MCP Go SDK docs).
    session, _ := client.Connect(context.Background(), transport, nil)

    toolsResult, _ := session.ListTools(context.Background(), nil)

    reg := registry.New(
        mcp.NewTools(toolsResult.Tools, session).
            WithMaxRetries(3).
            With(observable.WithOnRetry(func(attempt uint64, err error) {
                log.Printf("retry %d: %v", attempt, err)
            })).
            Build()...,
    )

    // Use reg.All() in your LLM request, reg.ByName() for dispatch.
}
```

---

## Test coverage

| Package | Coverage | Notes |
|---|---|---|
| `handler` | 100% | |
| `model` | 100% | |
| `observable` | 100% | |
| `registry` | 100% | |
| `schema` | 97.4% | Unknown-type fallback branch (`"string"`) unreachable via struct tags |
| `mcp` | 98.9% | Defensive `!ok` branch in `RegisterTools` unreachable at runtime |

Run tests:

```bash
go test -race ./...
```

---

## License

MIT — see [LICENSE](LICENSE).
