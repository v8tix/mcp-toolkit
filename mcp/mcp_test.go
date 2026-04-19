package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/v8tix/mcp-toolkit/handler"
	"github.com/v8tix/mcp-toolkit/model"
	"github.com/v8tix/mcp-toolkit/observable"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// mockSession is a controllable Session for unit tests.
type mockSession struct {
	fn func(ctx context.Context, params *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error)
}

func (m *mockSession) CallTool(ctx context.Context, params *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
	return m.fn(ctx, params)
}

func textResult(text string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}},
	}
}

func mcpTool(name, desc string, schema map[string]any) *sdkmcp.Tool {
	return &sdkmcp.Tool{Name: name, Description: desc, InputSchema: schema}
}

func schemaWith(props map[string]any, required []string) map[string]any {
	return map[string]any{"properties": props, "required": required}
}

func prop(typ, description string) map[string]any {
	return map[string]any{"type": typ, "description": description}
}

func propEnum(typ, description string, enum []string) map[string]any {
	return map[string]any{"type": typ, "description": description, "enum": enum}
}

// ── buildDefinition ───────────────────────────────────────────────────────────

func TestBuildDefinition_NameAndDescription(t *testing.T) {
	def := BuildDefinition(mcpTool("search", "Search the web.", nil))
	assert.Equal(t, "function", def.Type)
	assert.Equal(t, "search", def.Function.Name)
	assert.Equal(t, "Search the web.", def.Function.Description)
}

func TestBuildDefinition_AdditionalPropertiesAlwaysFalse(t *testing.T) {
	def := BuildDefinition(mcpTool("t", "d", nil))
	require.NotNil(t, def.Function.Parameters.AdditionalProperties)
	assert.False(t, *def.Function.Parameters.AdditionalProperties)
}

func TestBuildDefinition_PropertiesExtracted(t *testing.T) {
	schema := schemaWith(map[string]any{
		"query": prop("string", "Search query."),
		"limit": prop("integer", "Max results."),
	}, []string{"query"})

	def := BuildDefinition(mcpTool("search", "Search.", schema))
	params := def.Function.Parameters

	cases := []struct {
		name  string
		check func(*testing.T)
	}{
		{"query_type", func(t *testing.T) { assert.Equal(t, "string", params.Properties["query"].Type) }},
		{"query_desc", func(t *testing.T) { assert.Equal(t, "Search query.", params.Properties["query"].Description) }},
		{"limit_type", func(t *testing.T) { assert.Equal(t, "integer", params.Properties["limit"].Type) }},
		{"required_contains_query", func(t *testing.T) { assert.Contains(t, params.Required, "query") }},
		{"required_not_limit", func(t *testing.T) { assert.NotContains(t, params.Required, "limit") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.check(t) })
	}
}

func TestBuildDefinition_EnumExtracted(t *testing.T) {
	schema := schemaWith(map[string]any{
		"topic": propEnum("string", "Topic.", []string{"news", "finance"}),
	}, nil)

	def := BuildDefinition(mcpTool("search", "d", schema))
	assert.Equal(t, []string{"news", "finance"}, def.Function.Parameters.Properties["topic"].Enum)
}

func TestBuildDefinition_NilSchemaProducesEmptyParams(t *testing.T) {
	def := BuildDefinition(mcpTool("t", "d", nil))
	assert.Empty(t, def.Function.Parameters.Properties)
	assert.Empty(t, def.Function.Parameters.Required)
	assert.Equal(t, "object", def.Function.Parameters.Type)
}

func TestBuildDefinition_AdditionalProperties(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		want   bool
	}{
		{
			"absent_defaults_to_false",
			schemaWith(nil, nil),
			false,
		},
		{
			"explicit_false_preserved",
			func() map[string]any {
				s := schemaWith(nil, nil)
				s["additionalProperties"] = false
				return s
			}(),
			false,
		},
		{
			"explicit_true_preserved",
			func() map[string]any {
				s := schemaWith(nil, nil)
				s["additionalProperties"] = true
				return s
			}(),
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := BuildDefinition(mcpTool("t", "d", tc.schema))
			require.NotNil(t, def.Function.Parameters.AdditionalProperties)
			assert.Equal(t, tc.want, *def.Function.Parameters.AdditionalProperties)
		})
	}
}

func TestBuildDefinition_DefinitionTypeIsFunction(t *testing.T) {
	def := BuildDefinition(mcpTool("t", "d", nil))
	assert.Equal(t, "function", def.Type)
}

// ── rawMCPTool.Definition ─────────────────────────────────────────────────────

func TestRawMCPTool_DefinitionReturnedUnchanged(t *testing.T) {
	tool := mcpTool("greet", "Greet.", schemaWith(map[string]any{"name": prop("string", "Name.")}, []string{"name"}))
	raw := &rawMCPTool{
		def:     BuildDefinition(tool),
		session: &mockSession{},
		name:    tool.Name,
	}
	assert.Equal(t, raw.def, raw.Definition())
}

// ── rawMCPTool.Execute ────────────────────────────────────────────────────────

func TestRawMCPTool_Execute_HappyPath(t *testing.T) {
	var capturedParams *sdkmcp.CallToolParams
	session := &mockSession{fn: func(_ context.Context, p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		capturedParams = p
		return textResult(`{"answer":42}`), nil
	}}
	raw := &rawMCPTool{name: "calc", session: session, def: model.ToolDefinition{}}

	result, err := raw.Execute(context.Background(), []byte(`{"x":1}`))

	require.NoError(t, err)
	assert.Equal(t, `{"answer":42}`, result)
	assert.Equal(t, "calc", capturedParams.Name)
	assert.Equal(t, map[string]any{"x": float64(1)}, capturedParams.Arguments)
}

func TestRawMCPTool_Execute_InvalidJSON_SessionNotCalled(t *testing.T) {
	called := false
	session := &mockSession{fn: func(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		called = true
		return nil, nil
	}}
	raw := &rawMCPTool{name: "t", session: session, def: model.ToolDefinition{}}

	cases := []struct{ name, args string }{
		{"not_json", `not-json`},
		{"array_not_object", `[1,2]`},
		{"bare_string", `"hello"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := raw.Execute(context.Background(), []byte(tc.args))
			require.Error(t, err)
			assert.Contains(t, err.Error(), `"t"`)
		})
	}
	assert.False(t, called, "session must not be called when args are invalid JSON")
}

func TestRawMCPTool_Execute_CallToolError_Propagated(t *testing.T) {
	sentinel := errors.New("network timeout")
	session := &mockSession{fn: func(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		return nil, sentinel
	}}
	raw := &rawMCPTool{name: "t", session: session, def: model.ToolDefinition{}}

	_, err := raw.Execute(context.Background(), []byte(`{}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

func TestRawMCPTool_Execute_NilResult_ReturnsEmptyString(t *testing.T) {
	session := &mockSession{fn: func(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		return nil, nil
	}}
	raw := &rawMCPTool{name: "t", session: session, def: model.ToolDefinition{}}

	result, err := raw.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "", result)
}

func TestRawMCPTool_Execute_EmptyContent_ReturnsEmptyString(t *testing.T) {
	session := &mockSession{fn: func(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{}, nil
	}}
	raw := &rawMCPTool{name: "t", session: session, def: model.ToolDefinition{}}

	result, err := raw.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "", result)
}

func TestRawMCPTool_Execute_NonTextContent_Error(t *testing.T) {
	session := &mockSession{fn: func(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.ImageContent{Data: []byte("abc"), MIMEType: "image/png"}},
		}, nil
	}}
	raw := &rawMCPTool{name: "img", session: session, def: model.ToolDefinition{}}

	_, err := raw.Execute(context.Background(), []byte(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"img"`)
	assert.Contains(t, err.Error(), "unexpected content type")
}

// ── NewTool ───────────────────────────────────────────────────────────────────

func TestNewTool_ReturnsObservableTool(t *testing.T) {
	// *Builder must satisfy observable.Tool — assign to interface to verify at compile time.
	var tool observable.Tool = NewTool(mcpTool("t", "d", nil), &mockSession{}).WithMaxRetries(0)
	assert.NotNil(t, tool)
}

func TestNewTool_DefinitionMatchesMCPTool(t *testing.T) {
	mt := mcpTool("search", "Search the web.", schemaWith(
		map[string]any{"query": prop("string", "Query.")},
		[]string{"query"},
	))
	def := NewTool(mt, &mockSession{}).WithMaxRetries(0).Definition()

	assert.Equal(t, "search", def.Function.Name)
	assert.Equal(t, "Search the web.", def.Function.Description)
	assert.Equal(t, "string", def.Function.Parameters.Properties["query"].Type)
	assert.Contains(t, def.Function.Parameters.Required, "query")
}

func TestNewTool_ExecuteForwardsToSession(t *testing.T) {
	called := false
	session := &mockSession{fn: func(_ context.Context, p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		called = true
		assert.Equal(t, "search", p.Name)
		return textResult("ok"), nil
	}}
	result, err := NewTool(mcpTool("search", "d", nil), session).
		WithMaxRetries(0).
		Execute(context.Background(), []byte(`{}`))

	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "ok", result)
}

// ── NewTools ──────────────────────────────────────────────────────────────────

func TestNewTools_ReturnsCorrectCount(t *testing.T) {
	tools := NewTools([]*sdkmcp.Tool{
		mcpTool("a", "d", nil),
		mcpTool("b", "d", nil),
		mcpTool("c", "d", nil),
	}, &mockSession{}).WithMaxRetries(0).Build()
	assert.Len(t, tools, 3)
}

func TestNewTools_EachToolHasCorrectName(t *testing.T) {
	tools := NewTools([]*sdkmcp.Tool{
		mcpTool("alpha", "d", nil),
		mcpTool("beta", "d", nil),
	}, &mockSession{}).WithMaxRetries(0).Build()

	assert.Equal(t, "alpha", tools[0].Definition().Function.Name)
	assert.Equal(t, "beta", tools[1].Definition().Function.Name)
}

func TestNewTools_EmptySlice(t *testing.T) {
	tools := NewTools(nil, &mockSession{}).WithMaxRetries(0).Build()
	assert.Len(t, tools, 0)
}

// ── Builder.WithDefinition / WithDefinitionFunc ───────────────────────────────

func TestBuilder_WithDefinition_OverridesDefault(t *testing.T) {
	customDef := model.ToolDefinition{
		Type: "function",
		Function: model.FunctionDefinition{
			Name:        "custom_name",
			Description: "Custom description.",
		},
	}
	tool := NewTool(mcpTool("original", "Original.", nil), &mockSession{}).
		WithMaxRetries(0).
		WithDefinition(customDef)

	def := tool.Definition()
	assert.Equal(t, "custom_name", def.Function.Name)
	assert.Equal(t, "Custom description.", def.Function.Description)
}

func TestBuilder_WithDefinitionFunc_ReceivesMCPTool(t *testing.T) {
	var receivedTool *sdkmcp.Tool
	mt := mcpTool("search", "Search.", schemaWith(map[string]any{"q": prop("string", "Query.")}, []string{"q"}))

	tool := NewTool(mt, &mockSession{}).
		WithMaxRetries(0).
		WithDefinitionFunc(func(t *sdkmcp.Tool) model.ToolDefinition {
			receivedTool = t
			return BuildDefinition(t)
		})

	_ = tool.Definition()
	assert.Equal(t, mt, receivedTool)
}

func TestBuilder_WithDefinitionFunc_CustomDescription(t *testing.T) {
	mt := mcpTool("greet", "Hello.", nil)
	tool := NewTool(mt, &mockSession{}).
		WithMaxRetries(0).
		WithDefinitionFunc(func(t *sdkmcp.Tool) model.ToolDefinition {
			def := BuildDefinition(t)
			def.Function.Description = "[cached] " + def.Function.Description
			return def
		})

	assert.Equal(t, "[cached] Hello.", tool.Definition().Function.Description)
}

func TestBuilder_WithDefinitionFunc_DoesNotAffectSibling(t *testing.T) {
	mt := mcpTool("t", "d", nil)
	base := NewTool(mt, &mockSession{}).WithMaxRetries(0)
	withFn := base.WithDefinitionFunc(func(t *sdkmcp.Tool) model.ToolDefinition {
		def := BuildDefinition(t)
		def.Function.Description = "overridden"
		return def
	})

	assert.Equal(t, "d", base.Definition().Function.Description)
	assert.Equal(t, "overridden", withFn.Definition().Function.Description)
}

// ── ToolsBuilder.WithDefinitionFunc ──────────────────────────────────────────

func TestToolsBuilder_WithDefinitionFunc_AppliedToAll(t *testing.T) {
	tools := NewTools([]*sdkmcp.Tool{
		mcpTool("a", "desc-a", nil),
		mcpTool("b", "desc-b", nil),
	}, &mockSession{}).
		WithMaxRetries(0).
		WithDefinitionFunc(func(t *sdkmcp.Tool) model.ToolDefinition {
			def := BuildDefinition(t)
			def.Function.Description = "patched-" + def.Function.Description
			return def
		}).
		Build()

	assert.Equal(t, "patched-desc-a", tools[0].Definition().Function.Description)
	assert.Equal(t, "patched-desc-b", tools[1].Definition().Function.Description)
}

func TestToolsBuilder_WithDefinition_AllToolsGetSameDef(t *testing.T) {
	fixed := model.ToolDefinition{
		Type:     "function",
		Function: model.FunctionDefinition{Name: "fixed", Description: "fixed desc"},
	}
	tools := NewTools([]*sdkmcp.Tool{
		mcpTool("a", "d", nil),
		mcpTool("b", "d", nil),
	}, &mockSession{}).
		WithMaxRetries(0).
		WithDefinition(fixed).
		Build()

	for _, tool := range tools {
		assert.Equal(t, "fixed", tool.Definition().Function.Name)
	}
}

// ── Builder.With / ToolsBuilder.WithClassifier / ToolsBuilder.With ───────────

func TestBuilder_With_PassesOptionThrough(t *testing.T) {
	// With is a pass-through for arbitrary observable.Option values.
	// Verify it chains without panicking and the resulting tool is usable.
	session := &mockSession{fn: func(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		return textResult("ok"), nil
	}}
	tool := NewTool(mcpTool("t", "d", nil), session).
		With(observable.WithMaxRetries(0))

	result, err := tool.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
}

func TestToolsBuilder_WithClassifier_AppliedToAllTools(t *testing.T) {
	calls := 0
	session := &mockSession{fn: func(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		calls++
		return textResult("ok"), nil
	}}
	tools := NewTools([]*sdkmcp.Tool{
		mcpTool("a", "d", nil),
		mcpTool("b", "d", nil),
	}, session).
		WithClassifier(func(err error) error { return err }).
		WithMaxRetries(0).
		Build()

	for _, tool := range tools {
		exec := tool.(handler.ExecutableTool)
		_, err := exec.Execute(context.Background(), json.RawMessage(`{}`))
		require.NoError(t, err)
	}
	assert.Equal(t, 2, calls)
}

func TestToolsBuilder_With_PassesOptionThrough(t *testing.T) {
	session := &mockSession{fn: func(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		return textResult("ok"), nil
	}}
	tools := NewTools([]*sdkmcp.Tool{mcpTool("t", "d", nil)}, session).
		With(observable.WithMaxRetries(0)).
		Build()

	require.Len(t, tools, 1)
	exec := tools[0].(handler.ExecutableTool)
	result, err := exec.Execute(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
}
