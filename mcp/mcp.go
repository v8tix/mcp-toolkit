// Package mcp bridges the Model Context Protocol (MCP) SDK with the mcp-toolkit
// ecosystem. It wraps MCP server tools as observable.Tools so they can be
// registered in a registry.Registry and dispatched by the agent loop exactly
// like any other tool — including retry and exponential backoff on transient
// failures.
//
// Typical usage — register all tools discovered from an MCP server:
//
//	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "my-client", Version: "v1"}, nil)
//	session, _ := client.Connect(ctx, transport, nil)
//	toolsResult, _ := session.ListTools(ctx, nil)
//	reg := registry.New(mcp.NewTools(toolsResult.Tools, session).Build()...)
//
// Tune retry behavior per-tool or for all tools:
//
//	tool := mcp.NewTool(t, session).WithMaxRetries(5)
//	tools := mcp.NewTools(discovered, session).WithMaxRetries(0).Build()
//
// Each tool's Definition() is derived directly from the server's own metadata
// (name, description, input schema) — no duplication, no drift.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	rxgo "github.com/reactivex/rxgo/v2"
	"github.com/v8tix/mcp-toolkit/handler"
	"github.com/v8tix/mcp-toolkit/model"
	"github.com/v8tix/mcp-toolkit/observable"
)

// Session is the minimal interface required to call a tool on an MCP server.
// *sdkmcp.ClientSession satisfies this interface.
type Session interface {
	CallTool(ctx context.Context, params *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error)
}

// Option configures retry and error-classification behavior.
// All observable.Option values are accepted directly.
type Option = observable.Option

// Builder constructs a single observable.Tool from an MCP tool and session.
// Create one with NewTool; chain WithMaxRetries, WithClassifier, or With before use.
//
//	tool := mcp.NewTool(discovered, session).WithMaxRetries(3)
type Builder struct {
	mcpTool      *sdkmcp.Tool
	session      Session
	opts         []Option
	definitionFn func(*sdkmcp.Tool) model.ToolDefinition // nil = use BuildDefinition

	once  sync.Once
	built observable.Tool
}

var (
	_ observable.Tool        = (*Builder)(nil)
	_ handler.ExecutableTool = (*Builder)(nil)
	_ model.Tool             = (*Builder)(nil)
)

func (b *Builder) resolve() observable.Tool {
	b.once.Do(func() {
		defFn := b.definitionFn
		if defFn == nil {
			defFn = BuildDefinition
		}
		raw := &rawMCPTool{
			def:     defFn(b.mcpTool),
			session: b.session,
			name:    b.mcpTool.Name,
		}
		b.built = observable.Wrap(raw, b.opts...)
	})
	return b.built
}

func (b *Builder) clone(extra ...Option) *Builder {
	opts := make([]Option, len(b.opts)+len(extra))
	copy(opts, b.opts)
	copy(opts[len(b.opts):], extra)
	return &Builder{mcpTool: b.mcpTool, session: b.session, opts: opts, definitionFn: b.definitionFn}
}

// WithMaxRetries caps the number of retry attempts (exponential backoff).
//
//	mcp.NewTool(t, s).WithMaxRetries(0) // no retry
//	mcp.NewTool(t, s).WithMaxRetries(5) // up to 5 retries
func (b *Builder) WithMaxRetries(n uint64) *Builder {
	return b.clone(observable.WithMaxRetries(n))
}

// WithClassifier sets the error classifier.
// Return backoff.Permanent(err) to stop retrying immediately.
func (b *Builder) WithClassifier(fn func(error) error) *Builder {
	return b.clone(observable.WithClassifier(fn))
}

// With appends arbitrary observable.Option values for advanced configuration.
func (b *Builder) With(opts ...Option) *Builder {
	return b.clone(opts...)
}

// WithDefinition overrides the tool definition with a static value.
//
//	tool := mcp.NewTool(mt, s).WithDefinition(customDef)
func (b *Builder) WithDefinition(def model.ToolDefinition) *Builder {
	nb := b.clone()
	nb.definitionFn = func(_ *sdkmcp.Tool) model.ToolDefinition { return def }
	return nb
}

// WithDefinitionFunc overrides the tool definition using a function that
// receives the original *sdkmcp.Tool and returns a custom ToolDefinition.
//
//	tool := mcp.NewTool(mt, s).WithDefinitionFunc(func(t *sdkmcp.Tool) model.ToolDefinition {
//	    def := mcp.BuildDefinition(t)
//	    def.Function.Description = "[cached] " + def.Function.Description
//	    return def
//	})
func (b *Builder) WithDefinitionFunc(fn func(*sdkmcp.Tool) model.ToolDefinition) *Builder {
	nb := b.clone()
	nb.definitionFn = fn
	return nb
}

// Definition implements model.Tool.
func (b *Builder) Definition() model.ToolDefinition { return b.resolve().Definition() }

// Execute implements handler.ExecutableTool.
func (b *Builder) Execute(ctx context.Context, rawArgs json.RawMessage) (any, error) {
	return b.resolve().Execute(ctx, rawArgs)
}

// ExecuteRx implements observable.Tool.
func (b *Builder) ExecuteRx(ctx context.Context, rawArgs json.RawMessage) rxgo.Observable {
	return b.resolve().ExecuteRx(ctx, rawArgs)
}

// NewTool returns a Builder for a single MCP tool.
// Zero options applies production defaults (3 retries, exponential backoff).
//
//	tool := mcp.NewTool(discovered[0], session)
//	tool := mcp.NewTool(discovered[0], session).WithMaxRetries(5)
func NewTool(mcpTool *sdkmcp.Tool, session Session) *Builder {
	return &Builder{mcpTool: mcpTool, session: session}
}

// ToolsBuilder constructs multiple observable.Tools from a slice of MCP tools
// and a shared session. Create one with NewTools; chain options then call Build.
//
//	reg := registry.New(mcp.NewTools(discovered, session).WithMaxRetries(0).Build()...)
type ToolsBuilder struct {
	mcpTools     []*sdkmcp.Tool
	session      Session
	opts         []Option
	definitionFn func(*sdkmcp.Tool) model.ToolDefinition
}

// WithMaxRetries caps retries for all tools in the batch.
func (b *ToolsBuilder) WithMaxRetries(n uint64) *ToolsBuilder {
	return b.clone(observable.WithMaxRetries(n))
}

// WithClassifier sets the error classifier for all tools in the batch.
func (b *ToolsBuilder) WithClassifier(fn func(error) error) *ToolsBuilder {
	return b.clone(observable.WithClassifier(fn))
}

// With appends arbitrary observable.Option values for all tools in the batch.
func (b *ToolsBuilder) With(opts ...Option) *ToolsBuilder {
	return b.clone(opts...)
}

// WithDefinition overrides the definition for all tools in the batch with a
// static value. Use WithDefinitionFunc when you need per-tool customisation.
func (b *ToolsBuilder) WithDefinition(def model.ToolDefinition) *ToolsBuilder {
	nb := b.clone()
	nb.definitionFn = func(_ *sdkmcp.Tool) model.ToolDefinition { return def }
	return nb
}

// WithDefinitionFunc overrides the definition for all tools in the batch using
// a function that receives each tool's *sdkmcp.Tool.
func (b *ToolsBuilder) WithDefinitionFunc(fn func(*sdkmcp.Tool) model.ToolDefinition) *ToolsBuilder {
	nb := b.clone()
	nb.definitionFn = fn
	return nb
}

// Build returns a []model.Tool ready for registry.New:
//
//	reg := registry.New(mcp.NewTools(discovered, session).Build()...)
func (b *ToolsBuilder) Build() []model.Tool {
	result := make([]model.Tool, len(b.mcpTools))
	for i, t := range b.mcpTools {
		result[i] = &Builder{mcpTool: t, session: b.session, opts: b.opts, definitionFn: b.definitionFn}
	}
	return result
}

func (b *ToolsBuilder) clone(extra ...Option) *ToolsBuilder {
	opts := make([]Option, len(b.opts)+len(extra))
	copy(opts, b.opts)
	copy(opts[len(b.opts):], extra)
	return &ToolsBuilder{mcpTools: b.mcpTools, session: b.session, opts: opts, definitionFn: b.definitionFn}
}

// NewTools returns a ToolsBuilder for a batch of MCP tools sharing a session.
// Chain options then call Build to materialise the tools.
//
//	reg := registry.New(mcp.NewTools(discovered, session).WithMaxRetries(0).Build()...)
func NewTools(mcpTools []*sdkmcp.Tool, session Session) *ToolsBuilder {
	return &ToolsBuilder{mcpTools: mcpTools, session: session}
}

// rawMCPTool is the private, undecorated ExecutableTool. It is always wrapped
// by observable.Wrap before being returned to callers so the public surface
// only exposes observable.Tool (retry-aware).
type rawMCPTool struct {
	def     model.ToolDefinition
	session Session
	name    string
}

var _ handler.ExecutableTool = (*rawMCPTool)(nil)

func (t *rawMCPTool) Definition() model.ToolDefinition { return t.def }

// Execute forwards the raw JSON args to the MCP server and returns the
// plain-text content of the first TextContent item.
func (t *rawMCPTool) Execute(ctx context.Context, rawArgs json.RawMessage) (any, error) {
	var params map[string]any
	if err := json.Unmarshal(rawArgs, &params); err != nil {
		// Malformed args are permanent — retrying the same bytes always fails.
		return nil, observable.Permanent(fmt.Errorf("mcp tool %q: unmarshal args: %w", t.name, err))
	}

	result, err := t.session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      t.name,
		Arguments: params,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp tool %q: call failed: %w", t.name, err)
	}

	if result == nil || len(result.Content) == 0 {
		return "", nil
	}

	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok {
		return nil, fmt.Errorf("mcp tool %q: unexpected content type %T", t.name, result.Content[0])
	}

	return text.Text, nil
}

// BuildDefinition converts an MCP tool's metadata into a model.ToolDefinition.
// InputSchema is typed as any in the MCP SDK (a map[string]any on the client
// side), so we round-trip through JSON to extract properties and required fields.
//
// Use this as a starting point inside WithDefinitionFunc to derive the default
// definition and then customise it:
//
//	mcp.NewTool(t, s).WithDefinitionFunc(func(t *sdkmcp.Tool) model.ToolDefinition {
//	    def := mcp.BuildDefinition(t)
//	    def.Function.Description = "[cached] " + def.Function.Description
//	    return def
//	})
func BuildDefinition(mcpTool *sdkmcp.Tool) model.ToolDefinition {
	props := make(map[string]model.PropertySchema)
	var required []string

	raw, _ := json.Marshal(mcpTool.InputSchema)
	var schema struct {
		Properties map[string]struct {
			Type        string   `json:"type"`
			Description string   `json:"description"`
			Enum        []string `json:"enum,omitempty"`
		} `json:"properties"`
		Required             []string `json:"required"`
		AdditionalProperties *bool    `json:"additionalProperties,omitempty"`
	}
	if err := json.Unmarshal(raw, &schema); err == nil {
		for name, p := range schema.Properties {
			props[name] = model.PropertySchema{
				Type:        p.Type,
				Description: p.Description,
				Enum:        p.Enum,
			}
		}
		required = schema.Required
	}

	if schema.AdditionalProperties == nil {
		schema.AdditionalProperties = new(false)
	}

	return model.ToolDefinition{
		Type: "function",
		Function: model.FunctionDefinition{
			Name:        mcpTool.Name,
			Description: mcpTool.Description,
			Parameters: model.InputSchema{
				Type:                 "object",
				Properties:           props,
				Required:             required,
				AdditionalProperties: schema.AdditionalProperties,
			},
		},
	}
}
