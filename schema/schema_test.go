package schema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/v8tix/mcp-toolkit/model"
)

// ── NewInputSchemaFromStruct: validation ──────────────────────────────────────

func TestNewInputSchemaFromStruct_Validation(t *testing.T) {
	type sampleArgs struct {
		Query string `json:"query" desc:"A query."`
	}
	cases := []struct {
		name       string
		input      any
		wantPanic  bool
		panicValue any
	}{
		{
			name:       "nil panics with message",
			input:      nil,
			wantPanic:  true,
			panicValue: "mcptoolkit.NewInputSchemaFromStruct: v must not be nil",
		},
		{name: "string panics", input: "a string", wantPanic: true},
		{name: "int panics", input: 42, wantPanic: true},
		{name: "pointer_to_string_panics", input: new(string), wantPanic: true},
		{name: "pointer_to_int_panics", input: new(int), wantPanic: true},
		{name: "struct value accepted", input: sampleArgs{}, wantPanic: false},
		{name: "pointer to struct accepted", input: &sampleArgs{}, wantPanic: false},
		{name: "empty_struct_accepted", input: struct{}{}, wantPanic: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fn := func() { NewInputSchemaFromStruct(tc.input) }
			switch {
			case !tc.wantPanic:
				assert.NotPanics(t, fn)
			case tc.panicValue != nil:
				assert.PanicsWithValue(t, tc.panicValue, fn)
			default:
				assert.Panics(t, fn)
			}
		})
	}
}

// TestNewStrictTool_EquivalentToManual verifies NewStrictTool produces the same
// result as the explicit FormatToolDefinition + NewInputSchemaFromStruct form.
func TestNewStrictTool_EquivalentToManual(t *testing.T) {
	type sampleArgs struct {
		Query string `json:"query" desc:"The query."`
		Limit int    `json:"limit,omitempty" desc:"Max results."`
	}
	cases := []struct {
		name        string
		toolName    string
		description string
		args        any
	}{
		{name: "struct_value", toolName: "search", description: "Search.", args: sampleArgs{}},
		{name: "pointer_to_struct", toolName: "search", description: "Search.", args: &sampleArgs{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manual := FormatToolDefinition(
				tc.toolName, tc.description,
				NewInputSchemaFromStruct(tc.args),
				true,
			)
			shorthand := NewStrictTool(tc.toolName, tc.description, tc.args)
			assert.Equal(t, manual, shorthand)
		})
	}
}

// TestNonStrictToolOmitsStrictField verifies that when strict is false the
// "strict" field is absent from the serialized JSON (omitempty).
func TestNonStrictToolOmitsStrictField(t *testing.T) {
	def := FormatToolDefinition("t", "d", NewInputSchemaFromStruct(struct{}{}), false)
	assert.False(t, def.Function.Strict)
	data, err := json.Marshal(def)
	require.NoError(t, err)
	assert.NotContains(t, string(data), `"strict"`,
		"strict field must be absent from JSON when false (omitempty)")
}

// TestNewInputSchemaFromStruct_UnknownKindFallback verifies that a field whose
// Go kind has no entry in JSONSchemaKinds falls back to "string".
func TestNewInputSchemaFromStruct_UnknownKindFallback(t *testing.T) {
	type argsWithChan struct {
		Query string   `json:"query" desc:"search query"`
		Ch    chan int `json:"ch"    desc:"a channel"`
	}
	s := NewInputSchemaFromStruct(argsWithChan{})
	prop, ok := s.Properties["ch"]
	require.True(t, ok)
	assert.Equal(t, "string", prop.Type)
}

// TestNewInputSchemaFromStruct_DashTagSkipped verifies fields tagged json:"-"
// or json:"" are excluded from the schema.
func TestNewInputSchemaFromStruct_DashTagSkipped(t *testing.T) {
	type argsWithSkipped struct {
		Query string `json:"query" desc:"included"`
		Skip  string `json:"-"     desc:"excluded by dash"`
		Empty string `json:""      desc:"excluded by empty tag"`
	}
	s := NewInputSchemaFromStruct(argsWithSkipped{})
	assert.Contains(t, s.Properties, "query")
	_, hasDash := s.Properties["-"]
	assert.False(t, hasDash)
	_, hasSkip := s.Properties["Skip"]
	assert.False(t, hasSkip)
	_, hasEmpty := s.Properties["Empty"]
	assert.False(t, hasEmpty)
	assert.Equal(t, []string{"query"}, s.Required)
}

// TestNewInputSchemaFromStruct_UnexportedFieldsSkipped verifies unexported
// struct fields (no json tag) do not appear in the schema.
func TestNewInputSchemaFromStruct_UnexportedFieldsSkipped(t *testing.T) {
	type argsWithUnexported struct {
		Query   string `json:"query" desc:"included"`
		private string //nolint:unused
	}
	s := NewInputSchemaFromStruct(argsWithUnexported{})
	assert.Len(t, s.Properties, 1)
	assert.Contains(t, s.Properties, "query")
}

// TestNewInputSchemaFromStruct_RequiredFieldOrder verifies the required array
// follows struct field declaration order.
func TestNewInputSchemaFromStruct_RequiredFieldOrder(t *testing.T) {
	type orderedArgs struct {
		B string `json:"b" desc:"B field"`
		A string `json:"a" desc:"A field"`
		C string `json:"c" desc:"C field"`
	}
	s := NewInputSchemaFromStruct(orderedArgs{})
	assert.Equal(t, []string{"b", "a", "c"}, s.Required)
	assert.Len(t, s.Properties, 3)
}

// extractFromStruct mirrors NewInputSchemaFromStruct logic for independent
// validation in tests.
func extractFromStruct(v any) (map[string]model.PropertySchema, []string) {
	t := reflect.TypeOf(v)
	props := make(map[string]model.PropertySchema)
	var required []string
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		name := parts[0]
		omitempty := len(parts) > 1 && strings.Contains(parts[1], "omitempty")
		kind := f.Type.Kind()
		isPtr := kind == reflect.Ptr
		if isPtr {
			kind = f.Type.Elem().Kind()
		}
		jsonType, ok := JSONSchemaKinds[kind]
		if !ok {
			jsonType = "string"
		}
		desc := f.Tag.Get("desc")
		var enumVals []string
		if enumTag := f.Tag.Get("enum"); enumTag != "" {
			enumVals = strings.Split(enumTag, ",")
		}
		props[name] = model.PropertySchema{Type: jsonType, Description: desc, Enum: enumVals}
		if !isPtr && !omitempty {
			required = append(required, name)
		}
	}
	return props, required
}

// TestNewInputSchemaFromStruct_MatchesManualExtraction verifies schema output
// matches independent reflection-based extraction.
func TestNewInputSchemaFromStruct_MatchesManualExtraction(t *testing.T) {
	type sampleArgs struct {
		Query      string `json:"query"                 desc:"Search query"`
		MaxResults int    `json:"max_results,omitempty" desc:"Max results"`
		Topic      string `json:"topic,omitempty"       desc:"Topic" enum:"general,news"`
	}
	s := NewInputSchemaFromStruct(sampleArgs{})
	expectedProps, expectedRequired := extractFromStruct(sampleArgs{})

	assert.Equal(t, expectedProps, s.Properties)
	assert.Equal(t, expectedRequired, s.Required)
}
