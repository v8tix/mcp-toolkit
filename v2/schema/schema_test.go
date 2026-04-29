package schema

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── schema map helpers ────────────────────────────────────────────────────────

func schemaProps(s map[string]any) map[string]any {
	if p, ok := s["properties"]; ok {
		return p.(map[string]any)
	}
	return map[string]any{}
}

func schemaRequired(s map[string]any) []string {
	if r, ok := s["required"]; ok {
		return r.([]string)
	}
	return nil
}

func propField(s map[string]any, name string) map[string]any {
	props := schemaProps(s)
	if p, ok := props[name]; ok {
		return p.(map[string]any)
	}
	return nil
}

// ── InputSchema: validation ──────────────────────────────────────

func TestInputSchema_Validation(t *testing.T) {
	type sampleArgs struct {
		Query string `json:"query" description:"A query."`
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
			panicValue: "schema.Builder.InputSchema: v must not be nil",
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
			fn := func() { InputSchema(tc.input) }
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

// TestStrictTool_EqualsManualApplyStrict verifies StrictTool is equivalent to
// building a non-strict tool and then calling ApplyStrict manually — ensuring
// ApplyStrict is a valid escape hatch for custom schema workflows.
func TestStrictTool_EqualsManualApplyStrict(t *testing.T) {
	type sampleArgs struct {
		Query string `json:"query" description:"The query."`
		Limit int    `json:"limit,omitempty" description:"Max results."`
	}
	for _, args := range []any{sampleArgs{}, &sampleArgs{}} {
		shorthand := StrictTool("search", "Search.", args)

		manual := Default.Tool("search", "Search.", args)
		ApplyStrict(manual.InputSchema.(map[string]any))

		assert.Equal(t, shorthand.InputSchema, manual.InputSchema)
	}
}

// TestNonStrictTool_AdditionalPropertiesAbsent verifies that when strict is false
// the "additionalProperties" key is absent from the input schema.
func TestNonStrictTool_AdditionalPropertiesAbsent(t *testing.T) {
	def := Tool("t", "d", struct{}{})
	schema := def.InputSchema.(map[string]any)
	assert.NotContains(t, schema, "additionalProperties",
		"additionalProperties must be absent when strict is false")
}

// TestStrictTool_AdditionalPropertiesFalse verifies that strict mode sets
// additionalProperties to false in the schema.
func TestStrictTool_AdditionalPropertiesFalse(t *testing.T) {
	def := StrictTool("t", "d", struct{}{})
	schema := def.InputSchema.(map[string]any)
	require.Contains(t, schema, "additionalProperties")
	assert.Equal(t, false, schema["additionalProperties"])
}

// TestStrictTool_NestedObjectsAreStrict verifies that strict mode propagates
// additionalProperties: false to every nested object, satisfying OpenAI
// structured-output requirements for deeply nested schemas.
func TestStrictTool_NestedObjectsAreStrict(t *testing.T) {
	type address struct {
		City    string `json:"city"    description:"City name."`
		Country string `json:"country" description:"Country code."`
	}
	type args struct {
		Name    string  `json:"name"    description:"Full name."`
		Address address `json:"address" description:"Mailing address."`
	}
	def := StrictTool("t", "d", args{})
	top := def.InputSchema.(map[string]any)
	require.Equal(t, false, top["additionalProperties"], "top-level must be strict")

	props := top["properties"].(map[string]any)
	addrSchema := props["address"].(map[string]any)
	assert.Equal(t, false, addrSchema["additionalProperties"], "nested object must also be strict")
}

// TestNonStrictTool_NestedObjectsNotStrict verifies that non-strict mode leaves
// additionalProperties absent from all objects.
func TestNonStrictTool_NestedObjectsNotStrict(t *testing.T) {
	type address struct {
		City string `json:"city" description:"City name."`
	}
	type args struct {
		Address address `json:"address" description:"Mailing address."`
	}
	def := Tool("t", "d", args{})
	top := def.InputSchema.(map[string]any)
	assert.NotContains(t, top, "additionalProperties")

	props := top["properties"].(map[string]any)
	addrSchema := props["address"].(map[string]any)
	assert.NotContains(t, addrSchema, "additionalProperties")
}

// TestInputSchema_UnsupportedKindSkipped verifies that a field with an
// unsupported Go type (e.g. channel) is silently omitted from the schema.
func TestInputSchema_UnsupportedKindSkipped(t *testing.T) {
	type argsWithChan struct {
		Query string   `json:"query" description:"search query"`
		Ch    chan int `json:"ch"    description:"a channel"`
	}
	s := InputSchema(argsWithChan{})
	props := schemaProps(s)
	assert.Contains(t, props, "query", "valid field must be present")
	_, hasCh := props["ch"]
	assert.False(t, hasCh, "channel field must be omitted")
}

// TestInputSchema_DashTagSkipped verifies fields tagged json:"-" or json:""
// are excluded from the schema.
func TestInputSchema_DashTagSkipped(t *testing.T) {
	type argsWithSkipped struct {
		Query string `json:"query" description:"included"`
		Skip  string `json:"-"     description:"excluded by dash"`
		Empty string `json:""      description:"excluded by empty tag"`
	}
	s := InputSchema(argsWithSkipped{})
	props := schemaProps(s)
	assert.Contains(t, props, "query")
	_, hasDash := props["-"]
	assert.False(t, hasDash)
	_, hasSkip := props["Skip"]
	assert.False(t, hasSkip)
	_, hasEmpty := props["Empty"]
	assert.False(t, hasEmpty)
	assert.Equal(t, []string{"query"}, schemaRequired(s))
}

// TestInputSchema_UnexportedFieldsSkipped verifies unexported
// struct fields (no json tag) do not appear in the schema.
func TestInputSchema_UnexportedFieldsSkipped(t *testing.T) {
	type argsWithUnexported struct {
		Query   string `json:"query" description:"included"`
		private string //nolint:unused
	}
	s := InputSchema(argsWithUnexported{})
	props := schemaProps(s)
	assert.Len(t, props, 1)
	assert.Contains(t, props, "query")
}

// TestInputSchema_RequiredFieldOrder verifies the required array
// follows struct field declaration order.
func TestInputSchema_RequiredFieldOrder(t *testing.T) {
	type orderedArgs struct {
		B string `json:"b" description:"B field"`
		A string `json:"a" description:"A field"`
		C string `json:"c" description:"C field"`
	}
	s := InputSchema(orderedArgs{})
	assert.Equal(t, []string{"b", "a", "c"}, schemaRequired(s))
	assert.Len(t, schemaProps(s), 3)
}

// TestInputSchema_EmbeddedStructFlattened verifies that embedded
// (anonymous) struct fields are promoted to the top level, matching encoding/json.
func TestInputSchema_EmbeddedStructFlattened(t *testing.T) {
	type paginationArgs struct {
		Limit  int `json:"limit,omitempty"  description:"Max results."`
		Offset int `json:"offset,omitempty" description:"Results to skip."`
	}
	type searchArgs struct {
		Query string `json:"query" description:"Search query."`
		paginationArgs
	}
	s := InputSchema(searchArgs{})
	props := schemaProps(s)
	assert.Contains(t, props, "query")
	assert.Contains(t, props, "limit", "embedded field must be promoted")
	assert.Contains(t, props, "offset", "embedded field must be promoted")
	assert.Equal(t, []string{"query"}, schemaRequired(s))
}

// TestInputSchema_PropertyContents verifies the schema output
// contains the expected type, description, enum, and required entries.
func TestInputSchema_PropertyContents(t *testing.T) {
	type sampleArgs struct {
		Query      string `json:"query"                 description:"Search query"`
		MaxResults int    `json:"max_results,omitempty" description:"Max results"`
		Topic      string `json:"topic,omitempty"       description:"Topic" enum:"general,news"`
	}
	s := InputSchema(sampleArgs{})

	query := propField(s, "query")
	require.NotNil(t, query)
	assert.Equal(t, "string", query["type"])
	assert.Equal(t, "Search query", query["description"])

	maxResults := propField(s, "max_results")
	require.NotNil(t, maxResults)
	assert.Equal(t, "integer", maxResults["type"])
	assert.Equal(t, "Max results", maxResults["description"])

	topic := propField(s, "topic")
	require.NotNil(t, topic)
	assert.Equal(t, "Topic", topic["description"])
	assert.Equal(t, []string{"general", "news"}, topic["enum"])

	assert.Equal(t, []string{"query"}, schemaRequired(s))
}

// ── Builder options ───────────────────────────────────────────────────────────

// TestBuilder_WithTypeSchema verifies that a registered type override replaces
// the default schema for that Go type across the whole schema tree.
func TestBuilder_WithTypeSchema(t *testing.T) {
	type myID string
	idSchema := map[string]any{"type": "string", "format": "uuid"}

	b := NewBuilder().WithTypeSchema(reflect.TypeFor[myID](), idSchema)

	type args struct {
		ID   myID   `json:"id"   description:"Resource ID."`
		Name string `json:"name" description:"Display name."`
	}
	s := b.InputSchema(args{})
	props := schemaProps(s)

	idProp := props["id"].(map[string]any)
	assert.Equal(t, "string", idProp["type"])
	assert.Equal(t, "uuid", idProp["format"], "type override must inject format")

	nameProp := props["name"].(map[string]any)
	assert.Equal(t, "string", nameProp["type"])
	_, hasFormat := nameProp["format"]
	assert.False(t, hasFormat, "non-overridden type must not have format")
}

// TestBuilder_WithTypeSchema_ClonesMap verifies that mutating the override map
// after registration does not affect subsequently generated schemas.
func TestBuilder_WithTypeSchema_ClonesMap(t *testing.T) {
	type myID string
	override := map[string]any{"type": "string", "format": "uuid"}
	b := NewBuilder().WithTypeSchema(reflect.TypeFor[myID](), override)

	type args struct {
		ID myID `json:"id" description:"Resource ID."`
	}
	override["format"] = "tampered"
	s := b.InputSchema(args{})
	idProp := schemaProps(s)["id"].(map[string]any)
	assert.Equal(t, "uuid", idProp["format"], "override map must be cloned per-call, not shared")
}

// TestBuilder_ChainedWith_PreservesOverrides verifies that chaining a second
// With* method on a builder that already has type overrides correctly carries
// those overrides into the new builder (exercises the clone loop body).
func TestBuilder_ChainedWith_PreservesOverrides(t *testing.T) {
	type myID string
	idSchema := map[string]any{"type": "string", "format": "uuid"}

	b := NewBuilder().
		WithTypeSchema(reflect.TypeFor[myID](), idSchema).
		WithDescriptionTag("desc") // triggers clone on a builder with existing overrides

	type args struct {
		ID   myID   `json:"id"   desc:"Resource ID."`
		Name string `json:"name" desc:"Display name."`
	}
	s := b.InputSchema(args{})
	props := schemaProps(s)

	idProp := props["id"].(map[string]any)
	assert.Equal(t, "uuid", idProp["format"], "type override must survive chaining")
	assert.Equal(t, "Resource ID.", idProp["description"], "custom desc tag must also apply")
}

// TestBuilder_WithDescriptionTag verifies that a custom description tag name
// is honoured instead of the default "description" tag.
func TestBuilder_WithDescriptionTag(t *testing.T) {
	b := NewBuilder().WithDescriptionTag("desc")

	type args struct {
		Query string `json:"query" desc:"Search query." description:"ignored"`
	}
	s := b.InputSchema(args{})
	p := propField(s, "query")
	require.NotNil(t, p)
	assert.Equal(t, "Search query.", p["description"], "custom description tag must be used")
}

// TestBuilder_WithEnumTag verifies that a custom enum tag name is honoured
// instead of the default "enum" tag.
func TestBuilder_WithEnumTag(t *testing.T) {
	b := NewBuilder().WithEnumTag("oneof")

	type args struct {
		Topic string `json:"topic" oneof:"general,news" enum:"ignored"`
	}
	s := b.InputSchema(args{})
	p := propField(s, "topic")
	require.NotNil(t, p)
	assert.Equal(t, []string{"general", "news"}, p["enum"], "custom enum tag must be used")
}

// TestTypeSchema_AllPrimitives verifies every supported Go kind maps to the
// correct JSON Schema type keyword.
func TestTypeSchema_AllPrimitives(t *testing.T) {
	type allTypes struct {
		S  string  `json:"s"  description:"string"`
		B  bool    `json:"b"  description:"bool"`
		I  int     `json:"i"  description:"int"`
		F  float64 `json:"f"  description:"float"`
		U  uint32  `json:"u"  description:"uint"`
		F2 float32 `json:"f2" description:"float32"`
	}
	s := InputSchema(allTypes{})
	props := schemaProps(s)

	assert.Equal(t, "string", props["s"].(map[string]any)["type"])
	assert.Equal(t, "boolean", props["b"].(map[string]any)["type"])
	assert.Equal(t, "integer", props["i"].(map[string]any)["type"])
	assert.Equal(t, "number", props["f"].(map[string]any)["type"])
	assert.Equal(t, "integer", props["u"].(map[string]any)["type"])
	assert.Equal(t, "number", props["f2"].(map[string]any)["type"])
}

// TestTypeSchema_MapField verifies map fields generate {"type":"object"}.
func TestTypeSchema_MapField(t *testing.T) {
	type args struct {
		Meta map[string]string `json:"meta" description:"Metadata."`
	}
	s := InputSchema(args{})
	p := propField(s, "meta")
	require.NotNil(t, p)
	assert.Equal(t, "object", p["type"])
}

// TestTypeSchema_SliceOfStruct verifies a slice of structs generates an array
// schema whose items schema is the nested object schema.
func TestTypeSchema_SliceOfStruct(t *testing.T) {
	type item struct {
		Name string `json:"name" description:"Item name."`
	}
	type args struct {
		Items []item `json:"items" description:"List of items."`
	}
	s := InputSchema(args{})
	p := propField(s, "items")
	require.NotNil(t, p)
	assert.Equal(t, "array", p["type"])
	items, ok := p["items"].(map[string]any)
	require.True(t, ok, "items key must be a schema map")
	assert.Equal(t, "object", items["type"])
}

// TestApplyStrict_ArrayItems verifies ApplyStrict propagates
// additionalProperties:false into the items schema of array fields.
func TestApplyStrict_ArrayItems(t *testing.T) {
	type item struct {
		Name string `json:"name" description:"Name."`
	}
	type args struct {
		Items []item `json:"items" description:"Items."`
	}
	s := InputSchema(args{})
	ApplyStrict(s)

	p := propField(s, "items")
	require.NotNil(t, p)
	items := p["items"].(map[string]any)
	assert.Equal(t, false, items["additionalProperties"],
		"ApplyStrict must set additionalProperties:false on array item objects")
}

// TestApplyStrict_TopLevelArray verifies ApplyStrict handles a schema whose
// top-level type is "array" with object items (the bottom items branch).
func TestApplyStrict_TopLevelArray(t *testing.T) {
	arraySchema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": map[string]any{"type": "string"}},
		},
	}
	ApplyStrict(arraySchema)
	items := arraySchema["items"].(map[string]any)
	assert.Equal(t, false, items["additionalProperties"],
		"ApplyStrict must recurse into top-level array items")
}

// TestApplyStrict_NonMapPropertySkipped verifies ApplyStrict does not panic
// when a properties value is not a map (defensive continue branch).
func TestApplyStrict_NonMapPropertySkipped(t *testing.T) {
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"weird": "not-a-map", // non-map value
			"name":  map[string]any{"type": "string"},
		},
	}
	assert.NotPanics(t, func() { ApplyStrict(s) },
		"ApplyStrict must skip non-map property values without panicking")
}

// TestFieldSchema_EmptyNameSkipped verifies fields with a json tag that has no
// name (e.g. json:",omitempty") are excluded from the schema.
func TestFieldSchema_EmptyNameSkipped(t *testing.T) {
	type argsWithNoName struct {
		Valid  string `json:"valid"     description:"included"`
		NoName string `json:",omitempty" description:"excluded — no json name"`
	}
	s := InputSchema(argsWithNoName{})
	props := schemaProps(s)
	assert.Contains(t, props, "valid")
	_, hasNoName := props[""]
	assert.False(t, hasNoName, "field with no json name must be excluded")
	assert.Equal(t, []string{"valid"}, schemaRequired(s))
}
