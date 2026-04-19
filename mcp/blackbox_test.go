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
	"github.com/v8tix/mcp-toolkit/handler"
	"github.com/v8tix/mcp-toolkit/mcp"
	"github.com/v8tix/mcp-toolkit/model"
	"github.com/v8tix/mcp-toolkit/observable"
	"github.com/v8tix/mcp-toolkit/registry"
	"github.com/v8tix/mcp-toolkit/schema"
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

func (t schemaOnlyTool) Definition() model.ToolDefinition {
	type args struct {
		Input string `json:"input" desc:"input"`
	}
	return schema.NewStrictTool(t.name, "schema only — not executable", args{})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

type calcArgs struct {
	X float64 `json:"x" desc:"First operand."`
	Y float64 `json:"y" desc:"Second operand."`
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
	return &sdkmcp.Tool{
		Name:        name,
		Description: desc,
		InputSchema: map[string]any{"properties": props, "required": required},
	}
}

// executeRx drains an observable.Tool's ExecuteRx and returns the result or error.
// Retry lives in ExecuteRx, not Execute — mirror the reference repo pattern.
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

// TestRegisterTools_CallTool_ErrorIsToolError verifies that an execution error
// becomes a tool error (IsError=true) rather than a protocol-level error.
// The LLM can inspect the error text; the caller must not see a Go error.
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

	cases := []struct {
		name  string
		check func(*testing.T)
	}{
		{"name", func(t *testing.T) { assert.Equal(t, "search", def.Function.Name) }},
		{"description", func(t *testing.T) { assert.Equal(t, "Search the web.", def.Function.Description) }},
		{"query_type", func(t *testing.T) { assert.Equal(t, "string", def.Function.Parameters.Properties["query"].Type) }},
		{"limit_type", func(t *testing.T) { assert.Equal(t, "integer", def.Function.Parameters.Properties["limit"].Type) }},
		{"required_query", func(t *testing.T) { assert.Contains(t, def.Function.Parameters.Required, "query") }},
		{"required_not_limit", func(t *testing.T) { assert.NotContains(t, def.Function.Parameters.Required, "limit") }},
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
		{"absent_defaults_false", map[string]any{}, false},
		{"explicit_false", map[string]any{"additionalProperties": false}, false},
		{"explicit_true", map[string]any{"additionalProperties": true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mt := &sdkmcp.Tool{Name: "t", Description: "d", InputSchema: tc.schema}
			def := mcp.NewTool(mt, &stubSession{}).WithMaxRetries(0).Definition()
			require.NotNil(t, def.Function.Parameters.AdditionalProperties)
			assert.Equal(t, tc.want, *def.Function.Parameters.AdditionalProperties)
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

// TestNewTool_ExecuteRx_TransientError_Retried verifies that the observable layer
// retries transient failures and surfaces the result once the session recovers.
// Retry lives in ExecuteRx (not Execute) — mirrors the reference repo pattern.
// Default max retries is 3 — stub fails 2 times then succeeds.
func TestNewTool_ExecuteRx_TransientError_Retried(t *testing.T) {
	sentinel := errors.New("transient network error")
	session := &retryStubSession{failUntil: 2, sentinel: sentinel}
	tool := mcp.NewTool(mcpToolDef("t", "d", nil, nil), session)

	result, err := executeRx(context.Background(), tool, json.RawMessage(`{}`))

	require.NoError(t, err, "result must succeed after retries")
	assert.Equal(t, "recovered", result)
	assert.Equal(t, int32(3), session.calls.Load(),
		"session must be called 3 times: 2 failures + 1 success")
}

// TestNewTool_WithMaxRetries_0_NoRetry verifies that WithMaxRetries(0) disables
// the retry loop — the session is called exactly once even on failure.
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

// TestNewTool_WithClassifier_PermanentError_NoRetry verifies that a custom
// classifier marking every error as permanent suppresses all retries — the
// session is called exactly once regardless of MaxRetries.
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
	assert.Equal(t, "alpha", tools[0].Definition().Function.Name)
	assert.Equal(t, "Beta tool.", tools[1].Definition().Function.Description)
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

// TestNewTools_SpreadsIntoRegistry verifies the canonical usage pattern from
// the package doc: registry.New(mcp.NewTools(discovered, session)...)
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
	customDef := model.ToolDefinition{
		Type: "function",
		Function: model.FunctionDefinition{
			Name:        "renamed",
			Description: "Custom description.",
		},
	}
	mt := mcpToolDef("original", "Original.", nil, nil)
	def := mcp.NewTool(mt, &stubSession{}).WithMaxRetries(0).WithDefinition(customDef).Definition()

	assert.Equal(t, "renamed", def.Function.Name)
	assert.Equal(t, "Custom description.", def.Function.Description)
}

func TestNewTool_WithDefinitionFunc_TransformsDescription(t *testing.T) {
	mt := mcpToolDef("search", "Find things.", nil, nil)
	def := mcp.NewTool(mt, &stubSession{}).WithMaxRetries(0).
		WithDefinitionFunc(func(t *sdkmcp.Tool) model.ToolDefinition {
			d := model.ToolDefinition{
				Type: "function",
				Function: model.FunctionDefinition{
					Name:        t.Name,
					Description: "[v2] " + t.Description,
				},
			}
			return d
		}).
		Definition()

	assert.Equal(t, "search", def.Function.Name)
	assert.Equal(t, "[v2] Find things.", def.Function.Description)
}

func TestNewTool_WithDefinitionFunc_DoesNotMutateBase(t *testing.T) {
	mt := mcpToolDef("base", "Base desc.", nil, nil)
	base := mcp.NewTool(mt, &stubSession{}).WithMaxRetries(0)
	custom := base.WithDefinitionFunc(func(t *sdkmcp.Tool) model.ToolDefinition {
		return model.ToolDefinition{
			Type:     "function",
			Function: model.FunctionDefinition{Name: t.Name, Description: "modified"},
		}
	})

	assert.Equal(t, "Base desc.", base.Definition().Function.Description)
	assert.Equal(t, "modified", custom.Definition().Function.Description)
}

func TestNewTools_WithDefinitionFunc_AppliedToAllTools(t *testing.T) {
	discovered := []*sdkmcp.Tool{
		mcpToolDef("a", "desc-a", nil, nil),
		mcpToolDef("b", "desc-b", nil, nil),
	}
	tools := mcp.NewTools(discovered, &stubSession{}).
		WithMaxRetries(0).
		WithDefinitionFunc(func(t *sdkmcp.Tool) model.ToolDefinition {
			return model.ToolDefinition{
				Type: "function",
				Function: model.FunctionDefinition{
					Name:        t.Name,
					Description: "patched-" + t.Description,
				},
			}
		}).
		Build()

	require.Len(t, tools, 2)
	assert.Equal(t, "patched-desc-a", tools[0].Definition().Function.Description)
	assert.Equal(t, "patched-desc-b", tools[1].Definition().Function.Description)
}

func TestNewTools_WithDefinition_AllToolsGetSameStaticDef(t *testing.T) {
	fixed := model.ToolDefinition{
		Type:     "function",
		Function: model.FunctionDefinition{Name: "fixed", Description: "fixed desc"},
	}
	tools := mcp.NewTools([]*sdkmcp.Tool{
		mcpToolDef("a", "d", nil, nil),
		mcpToolDef("b", "d", nil, nil),
	}, &stubSession{}).
		WithMaxRetries(0).
		WithDefinition(fixed).
		Build()

	for _, tool := range tools {
		assert.Equal(t, "fixed", tool.Definition().Function.Name)
		assert.Equal(t, "fixed desc", tool.Definition().Function.Description)
	}
}
