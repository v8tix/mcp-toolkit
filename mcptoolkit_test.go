// Tests for the root mcptoolkit package, verifying that all re-exported symbols
// are reachable and behave correctly through a single import.
package mcptoolkit

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/v8tix/mcp-toolkit/handler"
	"github.com/v8tix/mcp-toolkit/model"
	"github.com/v8tix/mcp-toolkit/registry"
	"github.com/v8tix/mcp-toolkit/schema"
)

// ── Schema builders ──────────────────────────────────────────────────────────

type searchArgs struct {
	Query  string `json:"query"            desc:"The search query."`
	Limit  int    `json:"limit,omitempty"  desc:"Max number of results."`
	Format string `json:"format,omitempty" desc:"Output format." enum:"json,text"`
}

func TestNewStrictTool_ProducesValidDefinition(t *testing.T) {
	def := schema.NewStrictTool("search", "Search the web.", searchArgs{})

	assert.Equal(t, "function", def.Type)
	assert.Equal(t, "search", def.Function.Name)
	assert.Equal(t, "Search the web.", def.Function.Description)
	assert.True(t, def.Function.Strict)
	require.NotNil(t, def.Function.Parameters.AdditionalProperties)
	assert.False(t, *def.Function.Parameters.AdditionalProperties)
}

func TestNewStrictTool_RequiredAndOptionalFields(t *testing.T) {
	def := schema.NewStrictTool("search", "Search.", searchArgs{})
	params := def.Function.Parameters

	assert.Equal(t, []string{"query"}, params.Required,
		"only non-omitempty, non-pointer fields should be required")

	_, hasQuery := params.Properties["query"]
	_, hasLimit := params.Properties["limit"]
	_, hasFormat := params.Properties["format"]
	assert.True(t, hasQuery)
	assert.True(t, hasLimit)
	assert.True(t, hasFormat)
}

func TestNewStrictTool_EnumValues(t *testing.T) {
	def := schema.NewStrictTool("search", "Search.", searchArgs{})
	assert.Equal(t, []string{"json", "text"}, def.Function.Parameters.Properties["format"].Enum)
}

func TestFormatToolDefinition_NonStrictOmitsAdditionalProperties(t *testing.T) {
	params := schema.NewInputSchemaFromStruct(searchArgs{})
	def := schema.FormatToolDefinition("search", "Search.", params, false)

	assert.False(t, def.Function.Strict)
	assert.Nil(t, def.Function.Parameters.AdditionalProperties)
}

func TestFormatToolDefinition_EquivalentToNewStrictTool(t *testing.T) {
	manual := schema.FormatToolDefinition("search", "Search.", schema.NewInputSchemaFromStruct(searchArgs{}), true)
	shorthand := schema.NewStrictTool("search", "Search.", searchArgs{})
	assert.Equal(t, manual, shorthand)
}

func TestNewStrictTool_JSONRoundTrip(t *testing.T) {
	def := schema.NewStrictTool("search", "Search the web.", searchArgs{})

	data, err := json.Marshal(def)
	require.NoError(t, err)

	var roundTripped model.ToolDefinition
	require.NoError(t, json.Unmarshal(data, &roundTripped))
	assert.Equal(t, def, roundTripped)
}

// ── NewTool / execution ──────────────────────────────────────────────────────

type greetArgs struct {
	Name string `json:"name" desc:"Name to greet."`
}

func TestNewTool_DefinitionMatchesNewStrictTool(t *testing.T) {
	tool := handler.NewTool("greet", "Greet someone.",
		func(_ context.Context, in greetArgs) (string, error) {
			return "Hello, " + in.Name, nil
		})

	want := schema.NewStrictTool("greet", "Greet someone.", greetArgs{})
	assert.Equal(t, want, tool.Definition())
}

func TestNewTool_Execute(t *testing.T) {
	sentinel := errors.New("something went wrong")
	greetTool := func() handler.ExecutableTool {
		return handler.NewTool("greet", "Greet someone.",
			func(_ context.Context, in greetArgs) (string, error) {
				return "Hello, " + in.Name, nil
			})
	}

	cases := []struct {
		name    string
		tool    handler.ExecutableTool // nil → greetTool()
		rawArgs string
		wantVal any
		wantErr bool
		errIs   error
		errMsg  string
	}{
		{
			name:    "happy_path",
			rawArgs: `{"name":"World"}`,
			wantVal: "Hello, World",
		},
		{
			name:    "invalid_json",
			rawArgs: `not-json`,
			wantErr: true,
			errMsg:  `tool "greet"`,
		},
		{
			name: "handler_error",
			tool: handler.NewTool("greet", "Greet someone.",
				func(_ context.Context, _ greetArgs) (string, error) {
					return "", sentinel
				}),
			rawArgs: `{"name":"Alice"}`,
			wantErr: true,
			errIs:   sentinel,
		},
		{
			name:    "extra_fields_ignored",
			rawArgs: `{"name":"Alice","unknown":"ignored"}`,
			wantVal: "Hello, Alice",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := tc.tool
			if tool == nil {
				tool = greetTool()
			}
			result, err := tool.Execute(context.Background(), json.RawMessage(tc.rawArgs))
			if tc.wantErr {
				require.Error(t, err)
				if tc.errIs != nil {
					assert.ErrorIs(t, err, tc.errIs)
				}
				if tc.errMsg != "" {
					assert.Contains(t, err.Error(), tc.errMsg)
				}
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantVal, result)
			}
		})
	}
}

func TestNewTool_ImplementsTool(t *testing.T) {
	tool := handler.NewTool("greet", "Greet someone.",
		func(_ context.Context, in greetArgs) (string, error) {
			return "Hello, " + in.Name, nil
		})
	_, ok := tool.(model.Tool)
	assert.True(t, ok)
}

// ── Wrap / middleware ─────────────────────────────────────────────────────────

func TestWrap_DefinitionForwarded(t *testing.T) {
	inner := handler.NewTool("greet", "Greet someone.",
		func(_ context.Context, in greetArgs) (string, error) {
			return "Hello, " + in.Name, nil
		})
	wrapped := handler.Wrap(inner, func(ctx context.Context, raw json.RawMessage, next handler.ExecuteFunc) (any, error) {
		return next(ctx, raw)
	})
	assert.Equal(t, inner.Definition(), wrapped.Definition())
}

func TestWrap_MiddlewareExecutes(t *testing.T) {
	called := false
	inner := handler.NewTool("greet", "Greet someone.",
		func(_ context.Context, in greetArgs) (string, error) {
			return "Hello, " + in.Name, nil
		})
	wrapped := handler.Wrap(inner, func(ctx context.Context, raw json.RawMessage, next handler.ExecuteFunc) (any, error) {
		called = true
		return next(ctx, raw)
	})

	result, err := wrapped.Execute(context.Background(), json.RawMessage(`{"name":"Alice"}`))
	require.NoError(t, err)
	assert.Equal(t, "Hello, Alice", result)
	assert.True(t, called)
}

// ── Registry ─────────────────────────────────────────────────────────────────

func TestNew_RegisterAndRetrieve(t *testing.T) {
	tool := handler.NewTool("greet", "Greet someone.",
		func(_ context.Context, in greetArgs) (string, error) {
			return "Hello, " + in.Name, nil
		})

	reg := registry.New(tool)
	defs := reg.All()
	require.Len(t, defs, 1)
	assert.Equal(t, "greet", defs[0].Function.Name)
}

func TestNew_ByNameDispatch(t *testing.T) {
	tool := handler.NewTool("greet", "Greet someone.",
		func(_ context.Context, in greetArgs) (string, error) {
			return "Hello, " + in.Name, nil
		})

	reg := registry.New(tool)
	found, ok := reg.ByName("greet")
	require.True(t, ok)

	exec, ok := found.(handler.ExecutableTool)
	require.True(t, ok)
	result, err := exec.Execute(context.Background(), json.RawMessage(`{"name":"World"}`))
	require.NoError(t, err)
	assert.Equal(t, "Hello, World", result)
}

func TestNew_EmptyRegistry(t *testing.T) {
	reg := registry.New()
	assert.Len(t, reg.All(), 0)
	_, ok := reg.ByName("nope")
	assert.False(t, ok)
}

func TestNew_MultipleTools_InsertionOrder(t *testing.T) {
	makeSimple := func(name string) handler.ExecutableTool {
		return handler.NewTool(name, name+" tool.",
			func(_ context.Context, _ greetArgs) (string, error) { return name, nil })
	}

	reg := registry.New(makeSimple("alpha"), makeSimple("beta"), makeSimple("gamma"))
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, reg.Names())
}

func TestNew_EmptyNamePanics(t *testing.T) {
	assert.Panics(t, func() {
		registry.New(handler.NewTool("", "no name.",
			func(_ context.Context, _ greetArgs) (string, error) { return "", nil }))
	})
}

// ── Schema builders — edge cases ─────────────────────────────────────────────

func TestNewInputSchemaFromStruct_InvalidInputPanics(t *testing.T) {
	cases := []struct {
		name     string
		input    any
		panicMsg string // non-empty → assert exact message; empty → assert.Panics
	}{
		{
			name:     "nil",
			input:    nil,
			panicMsg: "mcptoolkit.NewInputSchemaFromStruct: v must not be nil",
		},
		{name: "string", input: "a string"},
		{name: "int", input: 42},
		{name: "pointer_to_string", input: new(string)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fn := func() { schema.NewInputSchemaFromStruct(tc.input) }
			if tc.panicMsg != "" {
				assert.PanicsWithValue(t, tc.panicMsg, fn)
			} else {
				assert.Panics(t, fn)
			}
		})
	}
}

func TestNewInputSchemaFromStruct_ValidInputs(t *testing.T) {
	t.Run("pointer_to_struct_equals_value", func(t *testing.T) {
		type args struct {
			Query string `json:"query" desc:"q"`
		}
		byVal := schema.NewInputSchemaFromStruct(args{})
		byPtr := schema.NewInputSchemaFromStruct(&args{})
		assert.Equal(t, byVal, byPtr, "pointer and value should produce identical schemas")
	})

	t.Run("empty_struct", func(t *testing.T) {
		s := schema.NewInputSchemaFromStruct(struct{}{})
		assert.Equal(t, "object", s.Type)
		assert.Empty(t, s.Properties)
		assert.Empty(t, s.Required)
	})

	t.Run("all_optional_nothing_required", func(t *testing.T) {
		type args struct {
			A string  `json:"a,omitempty" desc:"optional string"`
			B *string `json:"b"           desc:"pointer — always optional"`
		}
		s := schema.NewInputSchemaFromStruct(args{})
		assert.Empty(t, s.Required, "no field should be required when all are omitempty or pointer")
		assert.Len(t, s.Properties, 2)
	})
}

func TestNewStrictTool_StrictJSONField_AbsentWhenFalse(t *testing.T) {
	// Non-strict tool must NOT emit "strict":false in JSON (omitempty).
	def := schema.FormatToolDefinition("t", "d", schema.NewInputSchemaFromStruct(struct{}{}), false)
	data, err := json.Marshal(def)
	require.NoError(t, err)
	assert.NotContains(t, string(data), `"strict"`)
}

// ── NewTool / execution — edge cases ─────────────────────────────────────────

func TestNewTool_Execute_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tool := handler.NewTool("greet", "Greet someone.",
		func(ctx context.Context, in greetArgs) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return "Hello, " + in.Name, nil
		})

	result, err := tool.Execute(ctx, json.RawMessage(`{"name":"Alice"}`))
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, result)
}

func TestNewTool_Execute_EmptyObject(t *testing.T) {
	// {} is valid JSON — optional fields should get zero values.
	type args struct {
		Name string `json:"name,omitempty" desc:"name"`
	}
	tool := handler.NewTool("echo", "Echo name.",
		func(_ context.Context, in args) (string, error) {
			return in.Name, nil
		})
	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "", result)
}

// ── Wrap — edge cases ─────────────────────────────────────────────────────────

func TestWrap_ShortCircuit(t *testing.T) {
	innerCalled := false
	inner := handler.NewTool("greet", "Greet someone.",
		func(_ context.Context, _ greetArgs) (string, error) {
			innerCalled = true
			return "should not reach", nil
		})
	wrapped := handler.Wrap(inner,
		func(_ context.Context, _ json.RawMessage, _ handler.ExecuteFunc) (any, error) {
			return "cached", nil
		})

	result, err := wrapped.Execute(context.Background(), json.RawMessage(`{"name":"Alice"}`))
	require.NoError(t, err)
	assert.Equal(t, "cached", result)
	assert.False(t, innerCalled)
}

func TestWrap_MiddlewareInjectsError(t *testing.T) {
	sentinel := errors.New("rate limited")
	wrapped := handler.Wrap(
		handler.NewTool("greet", "Greet someone.",
			func(_ context.Context, in greetArgs) (string, error) { return "Hello", nil }),
		func(_ context.Context, _ json.RawMessage, _ handler.ExecuteFunc) (any, error) {
			return nil, sentinel
		},
	)

	result, err := wrapped.Execute(context.Background(), json.RawMessage(`{"name":"Alice"}`))
	assert.ErrorIs(t, err, sentinel)
	assert.Nil(t, result)
}

func TestWrap_StackedMiddlewareOrder(t *testing.T) {
	inner := handler.NewTool("greet", "Greet someone.",
		func(_ context.Context, in greetArgs) (string, error) { return "Hello, " + in.Name, nil })

	var order []string
	make := func(label string) handler.ToolMiddleware {
		return func(ctx context.Context, raw json.RawMessage, next handler.ExecuteFunc) (any, error) {
			order = append(order, label+":in")
			res, err := next(ctx, raw)
			order = append(order, label+":out")
			return res, err
		}
	}

	wrapped := handler.Wrap(handler.Wrap(inner, make("A")), make("B"))
	_, err := wrapped.Execute(context.Background(), json.RawMessage(`{"name":"X"}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"B:in", "A:in", "A:out", "B:out"}, order)
}

func TestWrap_ImplementsTool(t *testing.T) {
	wrapped := handler.Wrap(
		handler.NewTool("greet", "Greet someone.",
			func(_ context.Context, in greetArgs) (string, error) { return "", nil }),
		func(ctx context.Context, raw json.RawMessage, next handler.ExecuteFunc) (any, error) {
			return next(ctx, raw)
		},
	)
	_, ok := wrapped.(model.Tool)
	assert.True(t, ok)
}

// ── Registry — edge cases ─────────────────────────────────────────────────────

func TestNew_ByName_UnknownKeyReturnsFalse(t *testing.T) {
	reg := registry.New()
	_, ok := reg.ByName("does-not-exist")
	assert.False(t, ok)
}

func TestNew_DuplicateReplacement(t *testing.T) {
	v1 := handler.NewTool("greet", "version 1",
		func(_ context.Context, in greetArgs) (string, error) { return "v1", nil })
	v2 := handler.NewTool("greet", "version 2",
		func(_ context.Context, in greetArgs) (string, error) { return "v2", nil })

	reg := registry.New(v1, v2)
	defs := reg.All()
	require.Len(t, defs, 1, "duplicate name must replace, not append")
	assert.Equal(t, "version 2", defs[0].Function.Description)
}

func TestNew_AddAfterNew(t *testing.T) {
	reg := registry.New()
	reg.Add(handler.NewTool("alpha", "a",
		func(_ context.Context, _ greetArgs) (string, error) { return "", nil }))
	reg.Add(handler.NewTool("beta", "b",
		func(_ context.Context, _ greetArgs) (string, error) { return "", nil }))

	assert.Equal(t, []string{"alpha", "beta"}, reg.Names())
}

// ── End-to-end: define → register → dispatch ─────────────────────────────────

func TestEndToEnd_DefineRegisterDispatch(t *testing.T) {
	type addArgs struct {
		A int `json:"a" desc:"First operand."`
		B int `json:"b" desc:"Second operand."`
	}
	type addResult struct {
		Sum int `json:"sum"`
	}

	tool := handler.NewTool("add", "Add two integers.",
		func(_ context.Context, in addArgs) (addResult, error) {
			return addResult{Sum: in.A + in.B}, nil
		})

	reg := registry.New(tool)

	found, ok := reg.ByName("add")
	require.True(t, ok)

	exec, ok := found.(handler.ExecutableTool)
	require.True(t, ok)
	result, err := exec.Execute(context.Background(), json.RawMessage(`{"a":3,"b":4}`))
	require.NoError(t, err)
	assert.Equal(t, addResult{Sum: 7}, result)
}
