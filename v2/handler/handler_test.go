package handler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/v8tix/mcp-toolkit/v2/model"
	"github.com/v8tix/mcp-toolkit/v2/schema"
)

const (
	greetDesc     = "Greet someone."
	greetingAlice = "Hello, Alice"
)

// greetArgs is a minimal args struct used throughout handler tests.
type greetArgs struct {
	Name   string `json:"name"             description:"The name to greet."`
	Formal bool   `json:"formal,omitempty" description:"Use a formal greeting when true."`
}

func greetHandler(_ context.Context, in greetArgs) (string, error) {
	if in.Formal {
		return "Good day, " + in.Name, nil
	}
	return "Hello, " + in.Name, nil
}

// schemaMap extracts the InputSchema map from a *sdkmcp.Tool.
func schemaMap(t *testing.T, def *sdkmcp.Tool) map[string]any {
	t.Helper()
	m, ok := def.InputSchema.(map[string]any)
	require.True(t, ok, "InputSchema must be map[string]any")
	return m
}

// requiredFields extracts the "required" slice from a schema map.
func requiredFields(s map[string]any) []string {
	if r, ok := s["required"]; ok {
		return r.([]string)
	}
	return nil
}

// ── NewTool: schema ───────────────────────────────────────────────────────────

func TestNewTool_SchemaMatchesInputStruct(t *testing.T) {
	tool := NewTool("greet", greetDesc, greetHandler)
	def := tool.Definition()
	s := schemaMap(t, def)
	props := s["properties"].(map[string]any)

	cases := []struct {
		name  string
		check func(*testing.T)
	}{
		{"name", func(t *testing.T) { assert.Equal(t, "greet", def.Name) }},
		{"description", func(t *testing.T) { assert.Equal(t, greetDesc, def.Description) }},
		{"additionalProperties_is_false", func(t *testing.T) {
			require.Contains(t, s, "additionalProperties")
			assert.Equal(t, false, s["additionalProperties"])
		}},
		{"identical_to_StrictTool", func(t *testing.T) {
			want := schema.StrictTool("greet", greetDesc, greetArgs{})
			assert.Equal(t, want.Name, def.Name)
			assert.Equal(t, want.Description, def.Description)
			assert.Equal(t, want.InputSchema, def.InputSchema)
		}},
		{"required_contains_name", func(t *testing.T) {
			assert.Contains(t, requiredFields(s), "name")
		}},
		{"formal_is_optional", func(t *testing.T) {
			requiredSet := make(map[string]bool)
			for _, r := range requiredFields(s) {
				requiredSet[r] = true
			}
			assert.False(t, requiredSet["formal"])
		}},
		{"name_prop_exists", func(t *testing.T) {
			_, ok := props["name"]
			assert.True(t, ok)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.check(t) })
	}
}

func TestNewTool_DefinitionPrecomputed(t *testing.T) {
	tool := NewTool("greet", greetDesc, greetHandler)
	assert.Equal(t, tool.Definition(), tool.Definition())
}

func TestNewTool_ImplementsInterfaces(t *testing.T) {
	tool := NewTool("greet", greetDesc, greetHandler)
	_, isExecutable := tool.(ExecutableTool)
	assert.True(t, isExecutable)
	_, isTool := tool.(model.Tool)
	assert.True(t, isTool)
}

// ── NewTool: Execute — happy paths ────────────────────────────────────────────

func TestNewTool_Execute_Success(t *testing.T) {
	exec := NewTool("greet", greetDesc, greetHandler)

	cases := []struct {
		name    string
		rawArgs string
		want    string
	}{
		{"informal", `{"name":"Alice"}`, greetingAlice},
		{"formal", `{"name":"Bob","formal":true}`, "Good day, Bob"},
		{"extra_fields_ignored", `{"name":"Carol","unknown":"x"}`, "Hello, Carol"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := exec.Execute(context.Background(), json.RawMessage(tc.rawArgs))
			require.NoError(t, err)
			assert.Equal(t, tc.want, result)
		})
	}
}

// ── NewTool: Execute — error paths ────────────────────────────────────────────

func TestNewTool_Execute_InvalidJSON(t *testing.T) {
	called := false
	exec := NewTool("greet", greetDesc,
		func(_ context.Context, _ greetArgs) (string, error) {
			called = true
			return "", nil
		})

	cases := []struct {
		name    string
		rawArgs string
	}{
		{"not_json", `not-valid-json`},
		{"wrong_type", `"just a string"`},
		{"array_not_object", `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := exec.Execute(context.Background(), json.RawMessage(tc.rawArgs))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `tool "greet"`)
			assert.Nil(t, result)
		})
	}
	assert.False(t, called)
}

func TestNewTool_Execute_HandlerError(t *testing.T) {
	sentinel := errors.New("upstream service unavailable")
	exec := NewTool("greet", greetDesc,
		func(_ context.Context, _ greetArgs) (string, error) { return "", sentinel })

	result, err := exec.Execute(context.Background(), json.RawMessage(`{"name":"Alice"}`))
	assert.ErrorIs(t, err, sentinel)
	assert.Nil(t, result)
}

func TestNewTool_Execute_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	exec := NewTool("greet", greetDesc,
		func(ctx context.Context, in greetArgs) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return "Hello, " + in.Name, nil
		})

	result, err := exec.Execute(ctx, json.RawMessage(`{"name":"Alice"}`))
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, result)
}

// ── Decorator pattern (Wrap) ──────────────────────────────────────────────────

func TestWrap_ForwardsDefinitionUnchanged(t *testing.T) {
	inner := NewTool("greet", greetDesc, greetHandler)
	wrapped := Wrap(inner,
		func(ctx context.Context, rawArgs json.RawMessage, next ExecuteFunc) (any, error) {
			return next(ctx, rawArgs)
		})
	assert.Equal(t, inner.Definition(), wrapped.Definition())
}

func TestWrap_MiddlewareCanObserveArgsAndResult(t *testing.T) {
	inner := NewTool("greet", greetDesc, greetHandler)

	var observedArgs json.RawMessage
	var observedResult any
	wrapped := Wrap(inner,
		func(ctx context.Context, rawArgs json.RawMessage, next ExecuteFunc) (any, error) {
			observedArgs = rawArgs
			result, err := next(ctx, rawArgs)
			observedResult = result
			return result, err
		})

	rawArgs := json.RawMessage(`{"name":"Alice"}`)
	result, err := wrapped.Execute(context.Background(), rawArgs)

	require.NoError(t, err)
	assert.Equal(t, greetingAlice, result)
	assert.Equal(t, rawArgs, observedArgs)
	assert.Equal(t, greetingAlice, observedResult)
}

func TestWrap_MiddlewareCanShortCircuit(t *testing.T) {
	innerCalled := false
	inner := NewTool("greet", greetDesc,
		func(_ context.Context, _ greetArgs) (string, error) {
			innerCalled = true
			return "should not reach here", nil
		})
	wrapped := Wrap(inner,
		func(_ context.Context, _ json.RawMessage, _ ExecuteFunc) (any, error) {
			return "cached result", nil
		})

	result, err := wrapped.Execute(context.Background(), json.RawMessage(`{"name":"Bob"}`))

	require.NoError(t, err)
	assert.Equal(t, "cached result", result)
	assert.False(t, innerCalled)
}

func TestWrap_MiddlewareCanInjectError(t *testing.T) {
	rateLimitErr := errors.New("rate limit exceeded")
	wrapped := Wrap(
		NewTool("greet", greetDesc, greetHandler),
		func(_ context.Context, _ json.RawMessage, _ ExecuteFunc) (any, error) {
			return nil, rateLimitErr
		},
	)

	result, err := wrapped.Execute(context.Background(), json.RawMessage(`{"name":"Carol"}`))
	assert.ErrorIs(t, err, rateLimitErr)
	assert.Nil(t, result)
}

func TestWrap_StackingPreservesOrder(t *testing.T) {
	inner := NewTool("greet", greetDesc, greetHandler)

	var callOrder []string
	makeMiddleware := func(label string) ToolMiddleware {
		return func(ctx context.Context, rawArgs json.RawMessage, next ExecuteFunc) (any, error) {
			callOrder = append(callOrder, label+":before")
			result, err := next(ctx, rawArgs)
			callOrder = append(callOrder, label+":after")
			return result, err
		}
	}

	wrapped := Wrap(Wrap(inner, makeMiddleware("A")), makeMiddleware("B"))
	_, err := wrapped.Execute(context.Background(), json.RawMessage(`{"name":"Dave"}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"B:before", "A:before", "A:after", "B:after"}, callOrder)
}

func TestWrappedTool_Wrap_ChainsMiddlewareInOrder(t *testing.T) {
	inner := NewTool("greet", greetDesc, greetHandler)

	var callOrder []string
	makeMiddleware := func(label string) ToolMiddleware {
		return func(ctx context.Context, rawArgs json.RawMessage, next ExecuteFunc) (any, error) {
			callOrder = append(callOrder, label+":before")
			result, err := next(ctx, rawArgs)
			callOrder = append(callOrder, label+":after")
			return result, err
		}
	}

	wrapped := Wrap(inner, makeMiddleware("A")).
		Wrap(makeMiddleware("B"))

	result, err := wrapped.Execute(context.Background(), json.RawMessage(`{"name":"Dave"}`))

	require.NoError(t, err)
	assert.Equal(t, "Hello, Dave", result)
	assert.Equal(t, []string{"B:before", "A:before", "A:after", "B:after"}, callOrder)
}

func TestWrap_IsExecutableTool(t *testing.T) {
	wrapped := Wrap(
		NewTool("greet", greetDesc, greetHandler),
		func(ctx context.Context, rawArgs json.RawMessage, next ExecuteFunc) (any, error) {
			return next(ctx, rawArgs)
		},
	)
	var _ ExecutableTool = wrapped
	var _ model.Tool = wrapped
}

// ── NewToolWithDefinition ─────────────────────────────────────────────────────

func TestNewToolWithDefinition_UsesProvidedDefinition(t *testing.T) {
	customDef := schema.Tool(
		"custom_search",
		"Custom description.",
		greetArgs{},
	)
	tool := NewToolWithDefinition(customDef, greetHandler)
	assert.Equal(t, customDef, tool.Definition())
}

func TestNewToolWithDefinition_Execute_Works(t *testing.T) {
	def := schema.StrictTool("greet", greetDesc, greetArgs{})
	tool := NewToolWithDefinition(def, greetHandler)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"Alice"}`))
	require.NoError(t, err)
	assert.Equal(t, greetingAlice, result)
}

func TestNewToolWithDefinition_NonStrictMode(t *testing.T) {
	def := schema.Tool("greet", greetDesc, greetArgs{})
	tool := NewToolWithDefinition(def, greetHandler)

	s := schemaMap(t, def)
	assert.NotContains(t, s, "additionalProperties",
		"non-strict mode must not set additionalProperties")
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"Alice"}`))
	require.NoError(t, err)
	assert.Equal(t, greetingAlice, result)
}

func TestNewToolWithDefinition_DefinitionNotDerivedFromStruct(t *testing.T) {
	// Manually constructed definition — completely independent of struct tags.
	def := &sdkmcp.Tool{
		Name:        "manual_tool",
		Description: "Manually defined.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Name."},
			},
			"required": []string{"name"},
		},
	}
	tool := NewToolWithDefinition(def, greetHandler)
	assert.Equal(t, "manual_tool", tool.Definition().Name)
	assert.Equal(t, "Manually defined.", tool.Definition().Description)
}

func TestNewToolWithDefinition_InvalidArgs_ReturnsErrInvalidArguments(t *testing.T) {
	def := schema.StrictTool("greet", greetDesc, greetArgs{})
	tool := NewToolWithDefinition(def, greetHandler)

	_, err := tool.Execute(context.Background(), json.RawMessage(`not-json`))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidArguments)
}
