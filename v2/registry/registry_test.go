package registry

import (
	"sync"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/v8tix/mcp-toolkit/v2/model"
	"github.com/v8tix/mcp-toolkit/v2/schema"
)

// mockTool is a minimal model.Tool used in Registry tests.
type mockTool struct {
	toolName string
	desc     string
}

var _ model.Tool = mockTool{}

func (m mockTool) Definition() *sdkmcp.Tool {
	type args struct {
		Input string `json:"input" description:"test input"`
	}
	return schema.StrictTool(m.toolName, m.desc, args{})
}

// dynamicMockTool's Description is behind a pointer so it can change after registration.
type dynamicMockTool struct {
	toolName string
	desc     *string
}

var _ model.Tool = &dynamicMockTool{}

func (m *dynamicMockTool) Definition() *sdkmcp.Tool {
	type args struct {
		Input string `json:"input" description:"test input"`
	}
	return schema.StrictTool(m.toolName, *m.desc, args{})
}

// ── All ───────────────────────────────────────────────────────────────────────

func TestRegistry_All(t *testing.T) {
	a := mockTool{toolName: "tool_a", desc: "desc A"}
	b := mockTool{toolName: "tool_b", desc: "desc B"}

	cases := []struct {
		name  string
		check func(*testing.T)
	}{
		{"returns_correct_count", func(t *testing.T) {
			assert.Len(t, New(a).All(), 1)
		}},
		{"copy_safe_mutations_do_not_affect_registry", func(t *testing.T) {
			reg := New(a)
			snapshot := reg.All()
			snapshot[0] = nil
			assert.Equal(t, "tool_a", reg.All()[0].Name)
		}},
		{"empty_registry_returns_non_nil_slice", func(t *testing.T) {
			result := New().All()
			assert.NotNil(t, result)
			assert.Len(t, result, 0)
		}},
		{"multiple_tools_all_returned_in_insertion_order", func(t *testing.T) {
			r := New(a, b)
			defs := r.All()
			require.Len(t, defs, 2)
			assert.Equal(t, "tool_a", defs[0].Name)
			assert.Equal(t, "tool_b", defs[1].Name)
		}},
		{"duplicate_replace_updates_definition_in_all", func(t *testing.T) {
			v1 := mockTool{toolName: "tool_a", desc: "version 1"}
			v2 := mockTool{toolName: "tool_a", desc: "version 2"}
			r := New(v1, v2)
			defs := r.All()
			require.Len(t, defs, 1)
			assert.Equal(t, "version 2", defs[0].Description)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.check(t) })
	}
}

// ── Names ─────────────────────────────────────────────────────────────────────

func TestRegistry_Names(t *testing.T) {
	cases := []struct {
		name      string
		reg       *Registry
		wantNames []string
	}{
		{
			name:      "single_tool",
			reg:       New(mockTool{toolName: "alpha"}),
			wantNames: []string{"alpha"},
		},
		{
			name:      "two_distinct_tools_insertion_order",
			reg:       New(mockTool{toolName: "alpha"}, mockTool{toolName: "beta"}),
			wantNames: []string{"alpha", "beta"},
		},
		{
			name: "duplicate_replace_preserves_original_position",
			reg: func() *Registry {
				r := New(mockTool{toolName: "alpha"}, mockTool{toolName: "beta"})
				r.Add(mockTool{toolName: "alpha", desc: "updated"})
				return r
			}(),
			wantNames: []string{"alpha", "beta"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantNames, tc.reg.Names())
		})
	}
}

// ── ByName ────────────────────────────────────────────────────────────────────

func TestRegistry_ByName(t *testing.T) {
	reg := New(
		mockTool{toolName: "alpha", desc: "a"},
		mockTool{toolName: "beta", desc: "b"},
	)

	cases := []struct {
		query    string
		wantOk   bool
		wantName string
	}{
		{"alpha", true, "alpha"},
		{"beta", true, "beta"},
		{"does_not_exist", false, ""},
		{"", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			tool, ok := reg.ByName(tc.query)
			assert.Equal(t, tc.wantOk, ok)
			if tc.wantOk {
				assert.Equal(t, tc.wantName, tool.Definition().Name)
			}
		})
	}
}

// ── Deduplication ─────────────────────────────────────────────────────────────

func TestRegistry_Deduplication(t *testing.T) {
	cases := []struct {
		name    string
		add     []model.Tool
		wantLen int
	}{
		{"same_tool_twice_replaces", []model.Tool{mockTool{toolName: "x"}, mockTool{toolName: "x"}}, 1},
		{"one_unique_tool", []model.Tool{mockTool{toolName: "x"}}, 1},
		{"two_different_tools", []model.Tool{mockTool{toolName: "a"}, mockTool{toolName: "b"}}, 2},
		{"three_tools_one_duplicate", []model.Tool{mockTool{toolName: "x"}, mockTool{toolName: "y"}, mockTool{toolName: "x"}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New(tc.add...)
			assert.Len(t, r.All(), tc.wantLen)
		})
	}
}

func TestRegistry_Deduplication_ReplaceSemantics(t *testing.T) {
	cases := []struct {
		name        string
		setup       func() *Registry
		wantNames   []string
		wantDesc    string
		wantDescIdx int
	}{
		{
			name: "second_registration_wins_description",
			setup: func() *Registry {
				return New(
					mockTool{toolName: "tool_a", desc: "v1"},
					mockTool{toolName: "tool_a", desc: "v2"},
				)
			},
			wantNames: []string{"tool_a"}, wantDesc: "v2", wantDescIdx: 0,
		},
		{
			name: "replace_preserves_insertion_position",
			setup: func() *Registry {
				r := New(
					mockTool{toolName: "tool_a", desc: "v1"},
					mockTool{toolName: "tool_b", desc: "v1"},
				)
				r.Add(mockTool{toolName: "tool_a", desc: "v2"})
				return r
			},
			wantNames: []string{"tool_a", "tool_b"}, wantDesc: "v2", wantDescIdx: 0,
		},
		{
			name: "replace_middle_preserves_surrounding_order",
			setup: func() *Registry {
				r := New(
					mockTool{toolName: "first"},
					mockTool{toolName: "middle", desc: "v1"},
					mockTool{toolName: "last"},
				)
				r.Add(mockTool{toolName: "middle", desc: "v2"})
				return r
			},
			wantNames: []string{"first", "middle", "last"}, wantDesc: "v2", wantDescIdx: 1,
		},
		{
			name: "replace_last_preserves_preceding_order",
			setup: func() *Registry {
				r := New(
					mockTool{toolName: "alpha"},
					mockTool{toolName: "beta"},
					mockTool{toolName: "gamma", desc: "v1"},
				)
				r.Add(mockTool{toolName: "gamma", desc: "v2"})
				return r
			},
			wantNames: []string{"alpha", "beta", "gamma"}, wantDesc: "v2", wantDescIdx: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.setup()
			assert.Equal(t, tc.wantNames, r.Names())
			defs := r.All()
			require.True(t, tc.wantDescIdx < len(defs))
			assert.Equal(t, tc.wantDesc, defs[tc.wantDescIdx].Description)
		})
	}
}

// ── DefinitionCalledAtReadTime ────────────────────────────────────────────────

func TestRegistry_DefinitionCalledAtReadTime(t *testing.T) {
	desc := "version 1"
	tool := &dynamicMockTool{toolName: "dyn_tool", desc: &desc}
	r := New(tool)

	require.Equal(t, "version 1", r.All()[0].Description)
	desc = "version 2"
	assert.Equal(t, "version 2", r.All()[0].Description)
	assert.Equal(t, "dyn_tool", r.Names()[0])
}

// ── Empty name panics ─────────────────────────────────────────────────────────

func TestRegistry_Add_EmptyNamePanics(t *testing.T) {
	cases := []struct {
		name  string
		build func() func()
	}{
		{
			"New_with_empty_name",
			func() func() { return func() { New(mockTool{toolName: ""}) } },
		},
		{
			"Add_with_empty_name",
			func() func() {
				r := New()
				return func() { r.Add(mockTool{toolName: ""}) }
			},
		},
		{
			"fluent_Add_with_empty_name",
			func() func() {
				r := New(mockTool{toolName: "existing"})
				return func() { r.Add(mockTool{toolName: ""}) }
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.PanicsWithValue(t,
				"mcptoolkit.Registry.Add: tool name must not be empty",
				tc.build(),
				"Add must panic immediately when a tool's name is empty",
			)
		})
	}
}

// ── Remove ────────────────────────────────────────────────────────────────────

func TestRegistry_Remove(t *testing.T) {
	cases := []struct {
		name        string
		setup       func() *Registry
		removeName  string
		wantRemoved bool
		wantNames   []string
	}{
		{
			name:        "existing_first_tool",
			setup:       func() *Registry { return New(mockTool{toolName: "alpha"}, mockTool{toolName: "beta"}) },
			removeName:  "alpha",
			wantRemoved: true,
			wantNames:   []string{"beta"},
		},
		{
			name: "middle_tool_preserves_order",
			setup: func() *Registry {
				return New(mockTool{toolName: "first"}, mockTool{toolName: "middle"}, mockTool{toolName: "last"})
			},
			removeName:  "middle",
			wantRemoved: true,
			wantNames:   []string{"first", "last"},
		},
		{
			name:        "non_existent_returns_false",
			setup:       func() *Registry { return New(mockTool{toolName: "alpha"}) },
			removeName:  "does_not_exist",
			wantRemoved: false,
			wantNames:   []string{"alpha"},
		},
		{
			name:        "last_remaining_tool",
			setup:       func() *Registry { return New(mockTool{toolName: "only"}) },
			removeName:  "only",
			wantRemoved: true,
			wantNames:   []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.setup()
			removed := r.Remove(tc.removeName)
			assert.Equal(t, tc.wantRemoved, removed)
			assert.Equal(t, tc.wantNames, r.Names())
			if tc.wantRemoved {
				_, ok := r.ByName(tc.removeName)
				assert.False(t, ok, "removed tool must not be findable by name")
			}
		})
	}
}

func TestRegistry_Remove_ThenAdd_WorksCorrectly(t *testing.T) {
	r := New(mockTool{toolName: "alpha"})
	r.Remove("alpha")
	r.Add(mockTool{toolName: "alpha", desc: "re-added"})

	defs := r.All()
	require.Len(t, defs, 1)
	assert.Equal(t, "re-added", defs[0].Description)
}

// ── Filter ────────────────────────────────────────────────────────────────────

func TestRegistry_Filter(t *testing.T) {
	cases := []struct {
		name           string
		setup          func() *Registry
		fn             func(model.Tool) bool
		wantNames      []string
		checkOrigNames []string
	}{
		{
			name: "selects_matching_tools",
			setup: func() *Registry {
				return New(
					mockTool{toolName: "search_web"},
					mockTool{toolName: "internal_cache"},
					mockTool{toolName: "fetch_data"},
				)
			},
			fn: func(t model.Tool) bool {
				name := t.Definition().Name
				return len(name) < 10 || name[:6] != "intern"
			},
			wantNames: []string{"search_web", "fetch_data"},
		},
		{
			name:      "reject_all_returns_empty",
			setup:     func() *Registry { return New(mockTool{toolName: "alpha"}, mockTool{toolName: "beta"}) },
			fn:        func(_ model.Tool) bool { return false },
			wantNames: []string{},
		},
		{
			name:      "accept_all_same_content",
			setup:     func() *Registry { return New(mockTool{toolName: "alpha"}, mockTool{toolName: "beta"}) },
			fn:        func(_ model.Tool) bool { return true },
			wantNames: []string{"alpha", "beta"},
		},
		{
			name: "does_not_mutate_original",
			setup: func() *Registry {
				return New(mockTool{toolName: "alpha"}, mockTool{toolName: "beta"})
			},
			fn: func(t model.Tool) bool {
				return t.Definition().Name == "alpha"
			},
			wantNames:      []string{"alpha"},
			checkOrigNames: []string{"alpha", "beta"},
		},
		{
			name: "preserves_insertion_order",
			setup: func() *Registry {
				return New(mockTool{toolName: "c"}, mockTool{toolName: "a"}, mockTool{toolName: "b"})
			},
			fn:        func(_ model.Tool) bool { return true },
			wantNames: []string{"c", "a", "b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.setup()
			filtered := r.Filter(tc.fn)
			assert.Equal(t, tc.wantNames, filtered.Names())
			if tc.checkOrigNames != nil {
				assert.Equal(t, tc.checkOrigNames, r.Names(), "original registry must be unchanged")
			}
		})
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestRegistry_Concurrency(t *testing.T) {
	cases := []struct {
		name       string
		goroutines int
	}{
		{"10_goroutines", 10},
		{"50_goroutines", 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New(mockTool{toolName: "alpha"})
			var wg sync.WaitGroup
			for range tc.goroutines {
				wg.Add(2)
				go func() { defer wg.Done(); _ = r.All() }()
				go func() { defer wg.Done(); r.Add(mockTool{toolName: "alpha"}) }()
			}
			wg.Wait()
		})
	}
}

func TestRegistry_Concurrency_MultiTool(t *testing.T) {
	cases := []struct {
		name       string
		goroutines int
	}{
		{"10_goroutines_multi_tool", 10},
		{"50_goroutines_multi_tool", 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New(
				mockTool{toolName: "tool_a", desc: "A"},
				mockTool{toolName: "tool_b", desc: "B"},
			)
			var wg sync.WaitGroup
			for i := range tc.goroutines {
				wg.Add(3)
				go func() { defer wg.Done(); _ = r.All() }()
				go func() { defer wg.Done(); _ = r.Names() }()
				go func(n int) {
					defer wg.Done()
					name := "tool_a"
					if n%2 == 0 {
						name = "tool_b"
					}
					r.Add(mockTool{toolName: name, desc: "updated"})
				}(i)
			}
			wg.Wait()
		})
	}
}
