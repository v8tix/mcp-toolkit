package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/v8tix/mcp-toolkit/v2/handler"
	"github.com/v8tix/mcp-toolkit/v2/observable"
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

func mcpTool(name, desc string, inputSchema map[string]any) *sdkmcp.Tool {
	t := &sdkmcp.Tool{Name: name, Description: desc}
	if inputSchema != nil {
		t.InputSchema = inputSchema
	}
	return t
}

func schemaWith(props map[string]any, required []string) map[string]any {
	m := map[string]any{"properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func prop(typ, description string) map[string]any {
	return map[string]any{"type": typ, "description": description}
}

func propEnum(typ, description string, enum []string) map[string]any {
	return map[string]any{"type": typ, "description": description, "enum": enum}
}

// ── BuildDefinition ───────────────────────────────────────────────────────────

func TestBuildDefinition_NameAndDescription(t *testing.T) {
	def := BuildDefinition(mcpTool("search", "Search the web.", nil))
	assert.Equal(t, "search", def.Name)
	assert.Equal(t, "Search the web.", def.Description)
}

func TestBuildDefinition_NilSchema_SetsDefaultInputSchema(t *testing.T) {
	def := BuildDefinition(mcpTool("t", "d", nil))
	require.NotNil(t, def.InputSchema)
	s, ok := def.InputSchema.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "object", s["type"])
	props, ok := s["properties"].(map[string]any)
	require.True(t, ok)
	assert.Empty(t, props)
}

func TestBuildDefinition_NonNilSchema_PassedThrough(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"q": prop("string", "Query.")},
		"required":   []string{"q"},
	}
	original := mcpTool("search", "Search.", schema)
	def := BuildDefinition(original)
	// same pointer returned when InputSchema is non-nil
	assert.Equal(t, original, def)
}

func TestBuildDefinition_DoesNotMutateOriginal(t *testing.T) {
	original := mcpTool("t", "d", nil)
	_ = BuildDefinition(original)
	// original must still have nil InputSchema
	assert.Nil(t, original.InputSchema)
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
	raw := &rawMCPTool{name: "calc", session: session, def: &sdkmcp.Tool{Name: "calc"}}

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
	raw := &rawMCPTool{name: "t", session: session, def: &sdkmcp.Tool{Name: "t"}}

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
	raw := &rawMCPTool{name: "t", session: session, def: &sdkmcp.Tool{Name: "t"}}

	_, err := raw.Execute(context.Background(), []byte(`{}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

func TestRawMCPTool_Execute_NilResult_ReturnsEmptyString(t *testing.T) {
	session := &mockSession{fn: func(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		return nil, nil
	}}
	raw := &rawMCPTool{name: "t", session: session, def: &sdkmcp.Tool{Name: "t"}}

	result, err := raw.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "", result)
}

func TestRawMCPTool_Execute_EmptyContent_ReturnsEmptyString(t *testing.T) {
	session := &mockSession{fn: func(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{}, nil
	}}
	raw := &rawMCPTool{name: "t", session: session, def: &sdkmcp.Tool{Name: "t"}}

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
	raw := &rawMCPTool{name: "img", session: session, def: &sdkmcp.Tool{Name: "img"}}

	_, err := raw.Execute(context.Background(), []byte(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"img"`)
	assert.Contains(t, err.Error(), "unexpected content type")
}

// ── NewTool ───────────────────────────────────────────────────────────────────

func TestNewTool_ReturnsObservableTool(t *testing.T) {
	var tool observable.Tool = NewTool(mcpTool("t", "d", nil), &mockSession{}).WithMaxRetries(0)
	assert.NotNil(t, tool)
}

func TestNewTool_DefinitionMatchesMCPTool(t *testing.T) {
	mt := mcpTool("search", "Search the web.", schemaWith(
		map[string]any{"query": prop("string", "Query.")},
		[]string{"query"},
	))
	def := NewTool(mt, &mockSession{}).WithMaxRetries(0).Definition()

	assert.Equal(t, "search", def.Name)
	assert.Equal(t, "Search the web.", def.Description)
	s := def.InputSchema.(map[string]any)
	props := s["properties"].(map[string]any)
	assert.Equal(t, "string", props["query"].(map[string]any)["type"])
	required := s["required"].([]string)
	assert.Contains(t, required, "query")
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

	assert.Equal(t, "alpha", tools[0].Definition().Name)
	assert.Equal(t, "beta", tools[1].Definition().Name)
}

func TestNewTools_EmptySlice(t *testing.T) {
	tools := NewTools(nil, &mockSession{}).WithMaxRetries(0).Build()
	assert.Len(t, tools, 0)
}

// ── Builder.WithDefinition / WithDefinitionFunc ───────────────────────────────

func TestBuilder_WithDefinition_OverridesDefault(t *testing.T) {
	customDef := &sdkmcp.Tool{
		Name:        "custom_name",
		Description: "Custom description.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}
	tool := NewTool(mcpTool("original", "Original.", nil), &mockSession{}).
		WithMaxRetries(0).
		WithDefinition(customDef)

	def := tool.Definition()
	assert.Equal(t, "custom_name", def.Name)
	assert.Equal(t, "Custom description.", def.Description)
}

func TestBuilder_WithDefinitionFunc_ReceivesMCPTool(t *testing.T) {
	var receivedTool *sdkmcp.Tool
	mt := mcpTool("search", "Search.", schemaWith(map[string]any{"q": prop("string", "Query.")}, []string{"q"}))

	tool := NewTool(mt, &mockSession{}).
		WithMaxRetries(0).
		WithDefinitionFunc(func(t *sdkmcp.Tool) *sdkmcp.Tool {
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
		WithDefinitionFunc(func(t *sdkmcp.Tool) *sdkmcp.Tool {
			cp := *t
			cp.Description = "[cached] " + t.Description
			return &cp
		})

	assert.Equal(t, "[cached] Hello.", tool.Definition().Description)
}

func TestBuilder_WithDefinitionFunc_DoesNotAffectSibling(t *testing.T) {
	mt := mcpTool("t", "d", nil)
	base := NewTool(mt, &mockSession{}).WithMaxRetries(0)
	withFn := base.WithDefinitionFunc(func(t *sdkmcp.Tool) *sdkmcp.Tool {
		cp := *t
		cp.Description = "overridden"
		return &cp
	})

	assert.Equal(t, "d", base.Definition().Description)
	assert.Equal(t, "overridden", withFn.Definition().Description)
}

// ── ToolsBuilder.WithDefinitionFunc ──────────────────────────────────────────

func TestToolsBuilder_WithDefinitionFunc_AppliedToAll(t *testing.T) {
	tools := NewTools([]*sdkmcp.Tool{
		mcpTool("a", "desc-a", nil),
		mcpTool("b", "desc-b", nil),
	}, &mockSession{}).
		WithMaxRetries(0).
		WithDefinitionFunc(func(t *sdkmcp.Tool) *sdkmcp.Tool {
			cp := *t
			cp.Description = "patched-" + t.Description
			return &cp
		}).
		Build()

	assert.Equal(t, "patched-desc-a", tools[0].Definition().Description)
	assert.Equal(t, "patched-desc-b", tools[1].Definition().Description)
}

func TestToolsBuilder_WithDefinition_AllToolsGetSameDef(t *testing.T) {
	fixed := &sdkmcp.Tool{
		Name:        "fixed",
		Description: "fixed desc",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}
	tools := NewTools([]*sdkmcp.Tool{
		mcpTool("a", "d", nil),
		mcpTool("b", "d", nil),
	}, &mockSession{}).
		WithMaxRetries(0).
		WithDefinition(fixed).
		Build()

	for _, tool := range tools {
		assert.Equal(t, "fixed", tool.Definition().Name)
	}
}

// ── Builder.With / ToolsBuilder.WithClassifier / ToolsBuilder.With ───────────

func TestBuilder_With_PassesOptionThrough(t *testing.T) {
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
