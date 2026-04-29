// Package mcp_test exercises the mcp package from the outside — only exported
// identifiers. White-box tests (rawMCPTool, buildDefinition internals) live in
// bridge_test.go and mcp_test.go (package mcp).
package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cenkalti/backoff/v4"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/v8tix/mcp-toolkit/v2/handler"
	"github.com/v8tix/mcp-toolkit/v2/mcp"
	"github.com/v8tix/mcp-toolkit/v2/model"
	"github.com/v8tix/mcp-toolkit/v2/observable"
	"github.com/v8tix/mcp-toolkit/v2/registry"
	"github.com/v8tix/mcp-toolkit/v2/schema"
)

// ── Test doubles ──────────────────────────────────────────────────────────────

// stubSession is a controllable mcp.Session for black-box unit tests.
type stubSession struct {
	fn func(ctx context.Context, params *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error)
}

func (s *stubSession) CallTool(ctx context.Context, params *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
	return s.fn(ctx, params)
}

// retryStubSession fails the first failUntil calls, then returns ok text.
type retryStubSession struct {
	failUntil int32
	sentinel  error
	calls     atomic.Int32
}

func (s *retryStubSession) CallTool(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
	n := s.calls.Add(1)
	if n <= s.failUntil {
		return nil, s.sentinel
	}
	return textContent("recovered"), nil
}

// schemaOnlyTool satisfies model.Tool but NOT handler.ExecutableTool.
type schemaOnlyTool struct{ name string }

var _ model.Tool = schemaOnlyTool{}

func (t schemaOnlyTool) Definition() *sdkmcp.Tool {
	type args struct {
		Input string `json:"input" description:"input"`
	}
	return schema.StrictTool(t.name, "schema only — not executable", args{})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

type calcArgs struct {
	X float64 `json:"x" description:"First operand."`
	Y float64 `json:"y" description:"Second operand."`
}

type calcResult struct {
	Sum float64 `json:"sum"`
}

func sumHandler(_ context.Context, in calcArgs) (calcResult, error) {
	return calcResult{Sum: in.X + in.Y}, nil
}

func textContent(text string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}},
	}
}

func mcpToolDef(name, desc string, props map[string]any, required []string) *sdkmcp.Tool {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return &sdkmcp.Tool{Name: name, Description: desc, InputSchema: schema}
}

// executeRx drains an observable.Tool's ExecuteRx and returns the result or error.
func executeRx(ctx context.Context, tool observable.Tool, rawArgs json.RawMessage) (any, error) {
	var result any
	var execErr error
	for item := range tool.ExecuteRx(ctx, rawArgs).Observe() {
		if item.E != nil {
			execErr = item.E
		} else {
			result = item.V
		}
	}
	return result, execErr
}

// connectClient wires a server to an in-process client and returns the session.
func connectClient(t *testing.T, s *sdkmcp.Server) *sdkmcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, sTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { ss.Close() })
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, cTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	return cs
}

func newServer() *sdkmcp.Server {
	return sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-server", Version: "v0"}, nil)
}

// ── RegisterTools: tool visibility ───────────────────────────────────────────

func TestRegisterTools_ToolAppearsInListTools(t *testing.T) {
	s := newServer()
	reg := registry.New(
		handler.NewTool("add", "Add two numbers.", sumHandler),
	)
	mcp.RegisterTools(s, reg)

	cs := connectClient(t, s)
	result, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, result.Tools, 1)

	cases := []struct {
		name  string
		check func(*testing.T)
	}{
		{"name", func(t *testing.T) { assert.Equal(t, "add", result.Tools[0].Name) }},
		{"description", func(t *testing.T) { assert.Equal(t, "Add two numbers.", result.Tools[0].Description) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.check(t) })
	}
}

func TestRegisterTools_MultipleTools_AllVisible(t *testing.T) {
	s := newServer()
	reg := registry.New(
		handler.NewTool("add", "Add.", sumHandler),
		handler.NewTool("sub", "Subtract.", func(_ context.Context, in calcArgs) (calcResult, error) {
			return calcResult{Sum: in.X - in.Y}, nil
		}),
	)
	mcp.RegisterTools(s, reg)

	cs := connectClient(t, s)
	result, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	names := make([]string, len(result.Tools))
	for i, t := range result.Tools {
		names[i] = t.Name
	}
	assert.ElementsMatch(t, []string{"add", "sub"}, names)
}

func TestRegisterTools_NonExecutableToolNotVisible(t *testing.T) {
	s := newServer()
	mcp.RegisterTools(s, registry.New(schemaOnlyTool{name: "ghost"}))

	cs := connectClient(t, s)
	result, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, result.Tools,
		"non-executable tool must not be registered — client must not see it")
}

// ── RegisterTools: call tool ──────────────────────────────────────────────────

func TestRegisterTools_CallTool_ResultIsJSONText(t *testing.T) {
	s := newServer()
	mcp.RegisterTools(s, registry.New(
		handler.NewTool("add", "Add.", sumHandler),
	))

	cs := connectClient(t, s)
	result, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "add",
		Arguments: map[string]any{"x": 3.0, "y": 4.0},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	text, ok := result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok, "content must be TextContent")

	var got calcResult
	require.NoError(t, json.Unmarshal([]byte(text.Text), &got))
	assert.Equal(t, 7.0, got.Sum)
}

func TestRegisterTools_CallTool_ErrorIsToolError(t *testing.T) {
	sentinel := errors.New("upstream unavailable")
	s := newServer()
	mcp.RegisterTools(s, registry.New(
		handler.NewTool("fail", "Always fails.", func(_ context.Context, _ calcArgs) (calcResult, error) {
			return calcResult{}, sentinel
		}),
	))

	cs := connectClient(t, s)
	result, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "fail",
		Arguments: map[string]any{"x": 1.0, "y": 2.0},
	})

	require.NoError(t, err, "protocol error must be nil — execution errors are tool errors")
	assert.True(t, result.IsError, "IsError must be set so the LLM sees the failure")
}

// ── NewTool: definition ───────────────────────────────────────────────────────

func TestNewTool_Definition_MatchesMCPMetadata(t *testing.T) {
	mt := mcpToolDef("search", "Search the web.",
		map[string]any{
			"query": map[string]any{"type": "string", "description": "Search query."},
			"limit": map[string]any{"type": "integer", "description": "Max results."},
		},
		[]string{"query"},
	)
	def := mcp.NewTool(mt, &stubSession{}).WithMaxRetries(0).Definition()

	s := def.InputSchema.(map[string]any)
	props := s["properties"].(map[string]any)
	required := s["required"].([]string)

	cases := []struct {
		name  string
		check func(*testing.T)
	}{
		{"name", func(t *testing.T) { assert.Equal(t, "search", def.Name) }},
		{"description", func(t *testing.T) { assert.Equal(t, "Search the web.", def.Description) }},
		{"query_type", func(t *testing.T) {
			assert.Equal(t, "string", props["query"].(map[string]any)["type"])
		}},
		{"limit_type", func(t *testing.T) {
			assert.Equal(t, "integer", props["limit"].(map[string]any)["type"])
		}},
		{"required_query", func(t *testing.T) { assert.Contains(t, required, "query") }},
		{"required_not_limit", func(t *testing.T) { assert.NotContains(t, required, "limit") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.check(t) })
	}
}

func TestNewTool_AdditionalProperties_RespectedFromSchema(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		want   bool
	}{
		{"absent_passthrough", map[string]any{"type": "object", "properties": map[string]any{}}, false},
		{"explicit_false", map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}}, false},
		{"explicit_true", map[string]any{"type": "object", "additionalProperties": true, "properties": map[string]any{}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mt := &sdkmcp.Tool{Name: "t", Description: "d", InputSchema: tc.schema}
			def := mcp.NewTool(mt, &stubSession{}).WithMaxRetries(0).Definition()
			s := def.InputSchema.(map[string]any)
			if tc.want {
				assert.Equal(t, true, s["additionalProperties"])
			} else {
				v, ok := s["additionalProperties"]
				if ok {
					assert.Equal(t, false, v)
				}
			}
		})
	}
}

// ── NewTool: execution ────────────────────────────────────────────────────────

func TestNewTool_Execute_ForwardsToSession(t *testing.T) {
	var capturedName string
	var capturedArgs any

	session := &stubSession{fn: func(_ context.Context, p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		capturedName = p.Name
		capturedArgs = p.Arguments
		return textContent(`{"result":"ok"}`), nil
	}}
	tool := mcp.NewTool(mcpToolDef("search", "Search.", nil, nil), session).WithMaxRetries(0)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"go"}`))

	require.NoError(t, err)
	assert.Equal(t, "search", capturedName, "tool name must be forwarded to session")
	assert.Equal(t, map[string]any{"query": "go"}, capturedArgs)
	assert.Equal(t, `{"result":"ok"}`, result)
}

func TestNewTool_ExecuteRx_WithoutOptions_DoesNotRetry(t *testing.T) {
	sentinel := errors.New("transient network error")
	session := &retryStubSession{failUntil: 2, sentinel: sentinel}
	tool := mcp.NewTool(mcpToolDef("t", "d", nil, nil), session)

	result, err := executeRx(context.Background(), tool, json.RawMessage(`{}`))

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Equal(t, int32(1), session.calls.Load(),
		"session must be called exactly once when no retry options are configured")
}

func TestNewTool_WithMaxRetries_0_NoRetry(t *testing.T) {
	sentinel := errors.New("always fails")
	session := &retryStubSession{failUntil: 99, sentinel: sentinel}
	_, err := executeRx(context.Background(),
		mcp.NewTool(mcpToolDef("t", "d", nil, nil), session).WithMaxRetries(0),
		json.RawMessage(`{}`))

	require.Error(t, err)
	assert.Equal(t, int32(1), session.calls.Load(),
		"session must be called exactly once when retry is disabled")
}

func TestNewTool_WithMaxRetries_N_RetriesNTimes(t *testing.T) {
	cases := []struct {
		name      string
		maxRetry  uint64
		failUntil int32
		wantCalls int32
		wantErr   bool
	}{
		{"retry_1_fails_once_then_succeeds", 1, 1, 2, false},
		{"retry_2_exhausted", 2, 99, 3, true},
		{"retry_3_succeeds_on_3rd", 3, 2, 3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := errors.New("transient")
			session := &retryStubSession{failUntil: tc.failUntil, sentinel: sentinel}
			tool := mcp.NewTool(mcpToolDef("t", "d", nil, nil), session).WithMaxRetries(tc.maxRetry)

			_, err := executeRx(context.Background(), tool, json.RawMessage(`{}`))

			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.wantCalls, session.calls.Load())
		})
	}
}

func TestNewTool_WithClassifier_PermanentError_NoRetry(t *testing.T) {
	sentinel := errors.New("not found")
	session := &retryStubSession{failUntil: 99, sentinel: sentinel}

	tool := mcp.NewTool(mcpToolDef("t", "d", nil, nil), session).
		WithMaxRetries(5).
		WithClassifier(func(err error) error { return backoff.Permanent(err) })

	_, err := executeRx(context.Background(), tool, json.RawMessage(`{}`))

	require.Error(t, err)
	assert.Equal(t, int32(1), session.calls.Load(),
		"permanent classifier must prevent all retries — session called exactly once")
}

// ── NewTools ──────────────────────────────────────────────────────────────────

func TestNewTools_DefinitionsMatchMCPMetadata(t *testing.T) {
	discovered := []*sdkmcp.Tool{
		mcpToolDef("alpha", "Alpha tool.", nil, nil),
		mcpToolDef("beta", "Beta tool.", nil, nil),
	}
	session := &stubSession{fn: func(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		return textContent("ok"), nil
	}}
	tools := mcp.NewTools(discovered, session).WithMaxRetries(0).Build()

	require.Len(t, tools, 2)
	assert.Equal(t, "alpha", tools[0].Definition().Name)
	assert.Equal(t, "Beta tool.", tools[1].Definition().Description)
}

func TestNewTools_AllToolsCallable(t *testing.T) {
	var calls atomic.Int32
	session := &stubSession{fn: func(_ context.Context, p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)
		return textContent(p.Name + ":ok"), nil
	}}

	discovered := []*sdkmcp.Tool{
		mcpToolDef("a", "d", nil, nil),
		mcpToolDef("b", "d", nil, nil),
		mcpToolDef("c", "d", nil, nil),
	}
	tools := mcp.NewTools(discovered, session).WithMaxRetries(0).Build()

	for _, tool := range tools {
		exec := tool.(handler.ExecutableTool)
		_, err := exec.Execute(context.Background(), json.RawMessage(`{}`))
		require.NoError(t, err)
	}

	assert.Equal(t, int32(3), calls.Load(), "each tool must call the session once")
}

func TestNewTools_SpreadsIntoRegistry(t *testing.T) {
	session := &stubSession{fn: func(_ context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		return textContent("ok"), nil
	}}
	discovered := []*sdkmcp.Tool{
		mcpToolDef("search", "Search.", nil, nil),
		mcpToolDef("fetch", "Fetch.", nil, nil),
	}

	reg := registry.New(mcp.NewTools(discovered, session).WithMaxRetries(0).Build()...)

	assert.Equal(t, []string{"search", "fetch"}, reg.Names())

	t.Run("search_callable", func(t *testing.T) {
		tool, ok := reg.ByName("search")
		require.True(t, ok)
		exec := tool.(handler.ExecutableTool)
		_, err := exec.Execute(context.Background(), json.RawMessage(`{}`))
		assert.NoError(t, err)
	})
}

// ── WithDefinition / WithDefinitionFunc (black-box) ───────────────────────────

func TestNewTool_WithDefinition_OverridesDescription(t *testing.T) {
	customDef := &sdkmcp.Tool{
		Name:        "renamed",
		Description: "Custom description.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}
	mt := mcpToolDef("original", "Original.", nil, nil)
	def := mcp.NewTool(mt, &stubSession{}).WithMaxRetries(0).WithDefinition(customDef).Definition()

	assert.Equal(t, "renamed", def.Name)
	assert.Equal(t, "Custom description.", def.Description)
}

func TestNewTool_WithDefinitionFunc_TransformsDescription(t *testing.T) {
	mt := mcpToolDef("search", "Find things.", nil, nil)
	def := mcp.NewTool(mt, &stubSession{}).WithMaxRetries(0).
		WithDefinitionFunc(func(t *sdkmcp.Tool) *sdkmcp.Tool {
			cp := *t
			cp.Description = "[v2] " + t.Description
			return &cp
		}).
		Definition()

	assert.Equal(t, "search", def.Name)
	assert.Equal(t, "[v2] Find things.", def.Description)
}

func TestNewTool_WithDefinitionFunc_DoesNotMutateBase(t *testing.T) {
	mt := mcpToolDef("base", "Base desc.", nil, nil)
	base := mcp.NewTool(mt, &stubSession{}).WithMaxRetries(0)
	custom := base.WithDefinitionFunc(func(t *sdkmcp.Tool) *sdkmcp.Tool {
		cp := *t
		cp.Description = "modified"
		return &cp
	})

	assert.Equal(t, "Base desc.", base.Definition().Description)
	assert.Equal(t, "modified", custom.Definition().Description)
}

func TestNewTools_WithDefinitionFunc_AppliedToAllTools(t *testing.T) {
	discovered := []*sdkmcp.Tool{
		mcpToolDef("a", "desc-a", nil, nil),
		mcpToolDef("b", "desc-b", nil, nil),
	}
	tools := mcp.NewTools(discovered, &stubSession{}).
		WithMaxRetries(0).
		WithDefinitionFunc(func(t *sdkmcp.Tool) *sdkmcp.Tool {
			cp := *t
			cp.Description = "patched-" + t.Description
			return &cp
		}).
		Build()

	require.Len(t, tools, 2)
	assert.Equal(t, "patched-desc-a", tools[0].Definition().Description)
	assert.Equal(t, "patched-desc-b", tools[1].Definition().Description)
}

func TestNewTools_WithDefinition_AllToolsGetSameStaticDef(t *testing.T) {
	fixed := &sdkmcp.Tool{
		Name:        "fixed",
		Description: "fixed desc",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}
	tools := mcp.NewTools([]*sdkmcp.Tool{
		mcpToolDef("a", "d", nil, nil),
		mcpToolDef("b", "d", nil, nil),
	}, &stubSession{}).
		WithMaxRetries(0).
		WithDefinition(fixed).
		Build()

	for _, tool := range tools {
		assert.Equal(t, "fixed", tool.Definition().Name)
		assert.Equal(t, "fixed desc", tool.Definition().Description)
	}
}
