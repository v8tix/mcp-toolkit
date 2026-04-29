// Tests for the root mcptoolkit package, verifying that all re-exported symbols
// are reachable and behave correctly through a single import.
package mcptoolkit

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/v8tix/mcp-toolkit/v2/handler"
	"github.com/v8tix/mcp-toolkit/v2/model"
	"github.com/v8tix/mcp-toolkit/v2/registry"
	"github.com/v8tix/mcp-toolkit/v2/schema"
)

// ── Schema builders ──────────────────────────────────────────────────────────

type searchArgs struct {
	Query  string `json:"query"            description:"The search query."`
	Limit  int    `json:"limit,omitempty"  description:"Max number of results."`
	Format string `json:"format,omitempty" description:"Output format." enum:"json,text"`
}

// schemaOf extracts the InputSchema as map[string]any from a *sdkmcp.Tool.
func schemaOf(t *testing.T, def *sdkmcp.Tool) map[string]any {
	t.Helper()
	s, ok := def.InputSchema.(map[string]any)
	require.True(t, ok, "InputSchema must be map[string]any")
	return s
}

func TestStrictTool_ProducesValidDefinition(t *testing.T) {
	def := schema.StrictTool("search", "Search the web.", searchArgs{})

	assert.Equal(t, "search", def.Name)
	assert.Equal(t, "Search the web.", def.Description)

	s := schemaOf(t, def)
	assert.Equal(t, false, s["additionalProperties"], "strict tool must set additionalProperties=false")
}

func TestStrictTool_RequiredAndOptionalFields(t *testing.T) {
	def := schema.StrictTool("search", "Search.", searchArgs{})
	s := schemaOf(t, def)

	required, _ := s["required"].([]string)
	assert.Equal(t, []string{"query"}, required,
		"only non-omitempty, non-pointer fields should be required")

	props, _ := s["properties"].(map[string]any)
	_, hasQuery := props["query"]
	_, hasLimit := props["limit"]
	_, hasFormat := props["format"]
	assert.True(t, hasQuery)
	assert.True(t, hasLimit)
	assert.True(t, hasFormat)
}

func TestStrictTool_EnumValues(t *testing.T) {
	def := schema.StrictTool("search", "Search.", searchArgs{})
	s := schemaOf(t, def)
	props := s["properties"].(map[string]any)
	formatProp := props["format"].(map[string]any)
	assert.Equal(t, []string{"json", "text"}, formatProp["enum"])
}

func TestTool_NonStrictOmitsAdditionalProperties(t *testing.T) {
	def := schema.Tool("search", "Search.", searchArgs{})

	s := schemaOf(t, def)
	_, hasAdditionalProps := s["additionalProperties"]
	assert.False(t, hasAdditionalProps, "non-strict tool must not include additionalProperties")
}

func TestStrictTool_EqualsManualApplyStrict(t *testing.T) {
	shorthand := schema.StrictTool("search", "Search.", searchArgs{})

	manual := schema.Default.Tool("search", "Search.", searchArgs{})
	schema.ApplyStrict(manual.InputSchema.(map[string]any))

	assert.Equal(t, shorthand, manual)
}

func TestStrictTool_JSONRoundTrip(t *testing.T) {
	def := schema.StrictTool("search", "Search the web.", searchArgs{})

	data, err := json.Marshal(def)
	require.NoError(t, err)

	var roundTripped sdkmcp.Tool
	require.NoError(t, json.Unmarshal(data, &roundTripped))

	assert.Equal(t, def.Name, roundTripped.Name)
	assert.Equal(t, def.Description, roundTripped.Description)

	// InputSchema round-trips as map[string]interface{} — compare as JSON.
	originalSchema, err := json.Marshal(def.InputSchema)
	require.NoError(t, err)
	roundTrippedSchema, err := json.Marshal(roundTripped.InputSchema)
	require.NoError(t, err)
	assert.JSONEq(t, string(originalSchema), string(roundTrippedSchema))
}

// ── NewTool / execution ──────────────────────────────────────────────────────

type greetArgs struct {
	Name string `json:"name" description:"Name to greet."`
}

func TestNewTool_DefinitionMatchesStrictTool(t *testing.T) {
	tool := handler.NewTool("greet", "Greet someone.",
		func(_ context.Context, in greetArgs) (string, error) {
			return "Hello, " + in.Name, nil
		})

	want := schema.StrictTool("greet", "Greet someone.", greetArgs{})
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
	assert.Equal(t, "greet", defs[0].Name)
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

func TestInputSchema_InvalidInputPanics(t *testing.T) {
	cases := []struct {
		name     string
		input    any
		panicMsg string // non-empty → assert exact message; empty → assert.Panics
	}{
		{
			name:     "nil",
			input:    nil,
			panicMsg: "schema.Builder.InputSchema: v must not be nil",
		},
		{name: "string", input: "a string"},
		{name: "int", input: 42},
		{name: "pointer_to_string", input: new(string)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fn := func() { schema.InputSchema(tc.input) }
			if tc.panicMsg != "" {
				assert.PanicsWithValue(t, tc.panicMsg, fn)
			} else {
				assert.Panics(t, fn)
			}
		})
	}
}

func TestInputSchema_ValidInputs(t *testing.T) {
	t.Run("pointer_to_struct_equals_value", func(t *testing.T) {
		type args struct {
			Query string `json:"query" description:"q"`
		}
		byVal := schema.InputSchema(args{})
		byPtr := schema.InputSchema(&args{})
		assert.Equal(t, byVal, byPtr, "pointer and value should produce identical schemas")
	})

	t.Run("empty_struct", func(t *testing.T) {
		s := schema.InputSchema(struct{}{})
		assert.Equal(t, "object", s["type"])
		props, _ := s["properties"].(map[string]any)
		assert.Empty(t, props)
		_, hasRequired := s["required"]
		assert.False(t, hasRequired, "empty struct must not include required key")
	})

	t.Run("all_optional_nothing_required", func(t *testing.T) {
		type args struct {
			A string  `json:"a,omitempty" description:"optional string"`
			B *string `json:"b"           description:"pointer — always optional"`
		}
		s := schema.InputSchema(args{})
		_, hasRequired := s["required"]
		assert.False(t, hasRequired, "no field should be required when all are omitempty or pointer")
		props := s["properties"].(map[string]any)
		assert.Len(t, props, 2)
	})
}

func TestNonStrictTool_AdditionalPropertiesAbsent(t *testing.T) {
	def := schema.Tool("t", "d", struct{}{})
	s := schemaOf(t, def)
	_, hasAdditionalProps := s["additionalProperties"]
	assert.False(t, hasAdditionalProps, "non-strict tool must not emit additionalProperties")
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
		Name string `json:"name,omitempty" description:"name"`
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
	makeMW := func(label string) handler.ToolMiddleware {
		return func(ctx context.Context, raw json.RawMessage, next handler.ExecuteFunc) (any, error) {
			order = append(order, label+":in")
			res, err := next(ctx, raw)
			order = append(order, label+":out")
			return res, err
		}
	}

	wrapped := handler.Wrap(handler.Wrap(inner, makeMW("A")), makeMW("B"))
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
	var _ model.Tool = wrapped
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
	assert.Equal(t, "version 2", defs[0].Description)
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
		A int `json:"a" description:"First operand."`
		B int `json:"b" description:"Second operand."`
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
