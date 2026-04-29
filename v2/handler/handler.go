// Package handler provides generic, typed execution wrappers for LLM tool calls.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/v8tix/mcp-toolkit/v2/model"
	"github.com/v8tix/mcp-toolkit/v2/schema"
)

// ErrInvalidArguments is returned by Execute when the raw JSON arguments
// cannot be unmarshalled into the tool's typed input struct.
//
// This sentinel lets the observable layer (observable.ExecuteRx) stop retrying
// immediately — retrying the same malformed bytes will always produce the same
// unmarshal error, so consuming the retry budget is wasteful.
//
// Use errors.Is(err, handler.ErrInvalidArguments) to detect this case.
var ErrInvalidArguments = errors.New("invalid arguments")

// ToolHandler is the typed execution function every executable tool implements.
//
// In is the args struct populated by JSON-unmarshalling the LLM's tool-call
// arguments. Its fields map 1:1 to the tool's JSON Schema properties — the
// same struct drives both the schema (via NewTool) and the function signature.
//
// Out is the result type returned to the agent loop.
type ToolHandler[In any, Out any] func(ctx context.Context, in In) (Out, error)

// ExecutableTool extends model.Tool with an Execute method.
//
// The agent dispatch loop type-asserts to ExecutableTool after looking up a
// tool by name in the Registry:
//
//	tool, ok := reg.ByName(call.Name)
//	exec, ok := tool.(handler.ExecutableTool)
//	result, err := exec.Execute(ctx, call.Arguments)
//
// Use NewTool to construct an ExecutableTool from a typed ToolHandler.
type ExecutableTool interface {
	model.Tool
	// Execute unmarshals rawArgs into the tool's typed In struct, calls the
	// handler, and returns (result, error).
	Execute(ctx context.Context, rawArgs json.RawMessage) (any, error)
}

// typedTool[In, Out] is the private, generic implementation of ExecutableTool.
type typedTool[In any, Out any] struct {
	def     *sdkmcp.Tool
	handler ToolHandler[In, Out]
}

var _ ExecutableTool = (*typedTool[struct{}, struct{}])(nil)

func (t *typedTool[In, Out]) Definition() *sdkmcp.Tool { return t.def }

func (t *typedTool[In, Out]) Execute(ctx context.Context, rawArgs json.RawMessage) (any, error) {
	var in In
	if err := json.Unmarshal(rawArgs, &in); err != nil {
		return nil, fmt.Errorf("tool %q: %w: %w", t.def.Name, ErrInvalidArguments, err)
	}
	out, err := t.handler(ctx, in)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// NewTool creates an ExecutableTool from a name, description, and typed handler.
//
// The JSON Schema is derived once at construction time from In's struct tags
// (json, description, enum) via schema.StrictTool. Strict mode is always
// enabled. Panics if In is not a struct (or *struct).
//
// Example:
//
//	type SearchArgs struct {
//	    Query string `json:"query" description:"The search query."`
//	}
//	tool := handler.NewTool("search_web", "Search the web.",
//	    func(ctx context.Context, in SearchArgs) ([]Result, error) {
//	        return repo.Search(ctx, in.Query)
//	    },
//	)
func NewTool[In any, Out any](name, description string, handler ToolHandler[In, Out]) ExecutableTool {
	var zero In
	return &typedTool[In, Out]{
		def:     schema.StrictTool(name, description, zero),
		handler: handler,
	}
}

// NewToolWithDefinition creates an ExecutableTool using a caller-supplied
// *sdkmcp.Tool instead of deriving one from In's struct tags.
// Use this when you need non-strict mode, custom descriptions, or a schema
// that cannot be expressed via struct tags alone.
//
//	def := schema.Tool("search_web", "Search.", SearchArgs{})
//	tool := handler.NewToolWithDefinition(def, func(ctx context.Context, in SearchArgs) ([]Result, error) {
//	    return repo.Search(ctx, in.Query)
//	})
func NewToolWithDefinition[In any, Out any](def *sdkmcp.Tool, handler ToolHandler[In, Out]) ExecutableTool {
	return &typedTool[In, Out]{def: def, handler: handler}
}

// ── Decorator pattern ────────────────────────────────────────────────────────

// ExecuteFunc is the signature of the next hop in a middleware chain.
type ExecuteFunc func(ctx context.Context, rawArgs json.RawMessage) (any, error)

// ToolMiddleware is a function that wraps an Execute call.
// Middleware can add logging, retry, timeout, tracing, etc. without modifying
// the underlying tool.
//
// Example — a simple logger middleware:
//
//	func WithLogging(log func(string, ...any)) handler.ToolMiddleware {
//	    return func(ctx context.Context, rawArgs json.RawMessage, next handler.ExecuteFunc) (any, error) {
//	        log("tool call", "args", string(rawArgs))
//	        result, err := next(ctx, rawArgs)
//	        if err != nil { log("tool error", "error", err) }
//	        return result, err
//	    }
//	}
type ToolMiddleware func(ctx context.Context, rawArgs json.RawMessage, next ExecuteFunc) (any, error)

// WrappedTool is the Decorator produced by Wrap. Exported so callers can chain
// additional middleware via its Wrap method without a type assertion.
type WrappedTool struct {
	inner      ExecutableTool
	middleware ToolMiddleware
}

var _ ExecutableTool = (*WrappedTool)(nil)

func (w *WrappedTool) Definition() *sdkmcp.Tool { return w.inner.Definition() }
func (w *WrappedTool) Execute(ctx context.Context, rawArgs json.RawMessage) (any, error) {
	return w.middleware(ctx, rawArgs, w.inner.Execute)
}

// Wrap applies another ToolMiddleware on top of this one, enabling fluent
// chaining. The new middleware executes before (outside) the receiver.
//
//	tool := handler.Wrap(myTool, withTimeout(5*time.Second)).
//	    Wrap(withLogging(log.Printf))
func (w *WrappedTool) Wrap(middleware ToolMiddleware) *WrappedTool {
	return &WrappedTool{inner: w, middleware: middleware}
}

// Wrap applies a ToolMiddleware around an ExecutableTool's Execute method.
// The inner tool's Definition() is forwarded unchanged. Returns *WrappedTool
// so additional middleware can be chained via .Wrap().
//
//	tool := handler.Wrap(myTool, withTimeout(5*time.Second)).
//	    Wrap(withLogging(log.Printf)).
//	    Wrap(withMetrics(meter))
func Wrap(inner ExecutableTool, middleware ToolMiddleware) *WrappedTool {
	return &WrappedTool{inner: inner, middleware: middleware}
}
