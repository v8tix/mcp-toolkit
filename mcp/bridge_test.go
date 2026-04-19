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
	"github.com/v8tix/mcp-toolkit/registry"
	"github.com/v8tix/mcp-toolkit/schema"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

type greetArgs struct {
	Name string `json:"name" desc:"The name to greet."`
}

type greetResult struct {
	Message string `json:"message"`
}

func greetHandler(_ context.Context, in greetArgs) (greetResult, error) {
	return greetResult{Message: "Hello, " + in.Name}, nil
}

func errHandler(_ context.Context, _ greetArgs) (string, error) {
	return "", errors.New("something went wrong")
}

// nonExecutableTool satisfies model.Tool but not handler.ExecutableTool.
type nonExecutableTool struct{ name string }

var _ model.Tool = nonExecutableTool{}

func (t nonExecutableTool) Definition() model.ToolDefinition {
	type args struct {
		Input string `json:"input" desc:"input"`
	}
	return schema.NewStrictTool(t.name, "non-executable", args{})
}

// connectInMemory wires s to an in-process client and returns the ClientSession.
func connectInMemory(t *testing.T, s *sdkmcp.Server) *sdkmcp.ClientSession {
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

// ── RegisterTools: registration ───────────────────────────────────────────────

func TestRegisterTools_Registration(t *testing.T) {
	farewell := handler.NewTool("farewell", "Say goodbye.", func(_ context.Context, in greetArgs) (string, error) {
		return "Goodbye, " + in.Name, nil
	})

	cases := []struct {
		name      string
		reg       *registry.Registry
		wantCount int
		wantNames []string
	}{
		{
			name:      "empty_registry",
			reg:       registry.New(),
			wantCount: 0,
		},
		{
			name:      "single_executable_tool",
			reg:       registry.New(handler.NewTool("greet", "Greet someone.", greetHandler)),
			wantCount: 1,
			wantNames: []string{"greet"},
		},
		{
			name:      "multiple_executable_tools",
			reg:       registry.New(handler.NewTool("greet", "Greet someone.", greetHandler), farewell),
			wantCount: 2,
			wantNames: []string{"greet", "farewell"},
		},
		{
			name:      "non_executable_tool_skipped",
			reg:       registry.New(nonExecutableTool{name: "ghost"}),
			wantCount: 0,
		},
		{
			name: "mixed_executable_and_non_executable",
			reg: registry.New(
				handler.NewTool("greet", "Greet someone.", greetHandler),
				nonExecutableTool{name: "ghost"},
			),
			wantCount: 1,
			wantNames: []string{"greet"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer()
			RegisterTools(s, tc.reg)

			cs := connectInMemory(t, s)
			result, err := cs.ListTools(context.Background(), nil)
			require.NoError(t, err)
			require.Len(t, result.Tools, tc.wantCount)
			if len(tc.wantNames) > 0 {
				names := make([]string, len(result.Tools))
				for i, tool := range result.Tools {
					names[i] = tool.Name
				}
				assert.ElementsMatch(t, tc.wantNames, names)
			}
		})
	}
}

// ── RegisterTools: execution ──────────────────────────────────────────────────

func TestRegisterTools_CallTool_ReturnsJSONResult(t *testing.T) {
	s := newServer()
	RegisterTools(s, registry.New(
		handler.NewTool("greet", "Greet someone.", greetHandler),
	))

	cs := connectInMemory(t, s)
	result, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "greet",
		Arguments: map[string]any{"name": "Alice"},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "execution must not be flagged as error")
	require.Len(t, result.Content, 1)

	text, ok := result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok, "content must be TextContent")

	var got greetResult
	require.NoError(t, json.Unmarshal([]byte(text.Text), &got))
	assert.Equal(t, "Hello, Alice", got.Message)
}

func TestRegisterTools_CallTool_ExecutionErrorIsToolError(t *testing.T) {
	s := newServer()
	RegisterTools(s, registry.New(
		handler.NewTool("fail", "Always fails.", errHandler),
	))

	cs := connectInMemory(t, s)
	result, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "fail",
		Arguments: map[string]any{"name": "Bob"},
	})

	// Execution errors must surface as tool errors, not protocol-level errors.
	require.NoError(t, err, "protocol error must be nil — execution errors are tool errors")
	assert.True(t, result.IsError, "IsError must be true when Execute returns an error")
}

// ── RegisterTools: filter ─────────────────────────────────────────────────────

func TestRegisterTools_Filter(t *testing.T) {
	notInternal := func(t model.Tool) bool {
		return t.Definition().Function.Name[:8] != "internal"
	}
	notSearch := func(t model.Tool) bool {
		return t.Definition().Function.Name != "internal_search"
	}

	cases := []struct {
		name      string
		reg       *registry.Registry
		filters   []func(model.Tool) bool
		wantCount int
		wantNames []string
	}{
		{
			name: "no_filter_registers_all",
			reg: registry.New(
				handler.NewTool("a", "A.", greetHandler),
				handler.NewTool("b", "B.", greetHandler),
			),
			wantCount: 2,
			wantNames: []string{"a", "b"},
		},
		{
			name: "single_filter_excludes_tool",
			reg: registry.New(
				handler.NewTool("greet", "Greet.", greetHandler),
				handler.NewTool("farewell", "Farewell.", func(_ context.Context, in greetArgs) (string, error) {
					return "Bye, " + in.Name, nil
				}),
			),
			filters:   []func(model.Tool) bool{func(t model.Tool) bool { return t.Definition().Function.Name != "farewell" }},
			wantCount: 1,
			wantNames: []string{"greet"},
		},
		{
			name: "multiple_filters_all_must_pass",
			reg: registry.New(
				handler.NewTool("internal_search", "Internal.", greetHandler),
				handler.NewTool("public_greet", "Public.", greetHandler),
				handler.NewTool("internal_greet", "Internal greet.", greetHandler),
			),
			filters:   []func(model.Tool) bool{notInternal, notSearch},
			wantCount: 1,
			wantNames: []string{"public_greet"},
		},
		{
			name:      "reject_all_nothing_registered",
			reg:       registry.New(handler.NewTool("greet", "Greet.", greetHandler)),
			filters:   []func(model.Tool) bool{func(_ model.Tool) bool { return false }},
			wantCount: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer()
			RegisterTools(s, tc.reg, tc.filters...)

			cs := connectInMemory(t, s)
			result, err := cs.ListTools(context.Background(), nil)
			require.NoError(t, err)
			require.Len(t, result.Tools, tc.wantCount)
			if len(tc.wantNames) > 0 {
				names := make([]string, len(result.Tools))
				for i, tool := range result.Tools {
					names[i] = tool.Name
				}
				assert.ElementsMatch(t, tc.wantNames, names)
			}
		})
	}
}

func TestRegisterTools_CallTool_StringResultEncoded(t *testing.T) {
	s := newServer()
	RegisterTools(s, registry.New(
		handler.NewTool("echo", "Echo the name.", func(_ context.Context, in greetArgs) (string, error) {
			return in.Name, nil
		}),
	))

	cs := connectInMemory(t, s)
	result, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"name": "Carol"},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	text, ok := result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)

	var got string
	require.NoError(t, json.Unmarshal([]byte(text.Text), &got))
	assert.Equal(t, "Carol", got)
}
