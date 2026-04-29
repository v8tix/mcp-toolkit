// Package schema provides LLM-tool–oriented JSON Schema builders for Go structs.
// It generates the JSON Schema subset required by OpenAI, Anthropic, Gemini, and
// other LLM APIs that accept tool definitions.
//
// Quick start — use the package-level shortcuts backed by the Default builder:
//
//	tool := schema.StrictTool("search_web", "Search the web.", SearchArgs{})
//
// Advanced — create a custom Builder to override type schemas or tag names:
//
//	b := schema.NewBuilder(
//	    schema.WithTypeSchema(reflect.TypeFor[uuid.UUID](), map[string]any{
//	        "type": "string", "format": "uuid",
//	    }),
//	)
//	tool := b.StrictTool("create_user", "Create a user.", UserArgs{})
package schema

import (
	"fmt"
	"reflect"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ── Builder ───────────────────────────────────────────────────────────────────

// Builder generates LLM-compatible JSON Schema from annotated Go structs.
// Construct one with NewBuilder and chain With* methods to configure it.
// Use the package-level shortcuts (StrictTool, Tool, InputSchema) for the
// common case where no customisation is needed.
//
//	b := schema.NewBuilder().
//	    WithTypeSchema(reflect.TypeFor[uuid.UUID](), map[string]any{"type":"string","format":"uuid"}).
//	    WithDescriptionTag("desc")
//	tool := b.StrictTool("create_user", "Create a user.", UserArgs{})
type Builder struct {
	typeOverrides map[reflect.Type]map[string]any
	descTag       string
	enumTag       string
}

// NewBuilder returns a Builder with default settings:
// description tag "description", enum tag "enum", no type overrides.
func NewBuilder() *Builder {
	return &Builder{
		typeOverrides: make(map[reflect.Type]map[string]any),
		descTag:       "description",
		enumTag:       "enum",
	}
}

// WithTypeSchema returns a new Builder that overrides the JSON Schema emitted
// for a specific Go type. The provided map is cloned so the caller may safely
// reuse or mutate it after the call.
//
//	b := schema.NewBuilder().
//	    WithTypeSchema(reflect.TypeFor[uuid.UUID](), map[string]any{"type":"string","format":"uuid"}).
//	    WithTypeSchema(reflect.TypeFor[time.Time](), map[string]any{"type":"string","format":"date-time"})
func (b *Builder) WithTypeSchema(t reflect.Type, s map[string]any) *Builder {
	next := b.clone()
	clone := make(map[string]any, len(s))
	for k, v := range s {
		clone[k] = v
	}
	next.typeOverrides[t] = clone
	return next
}

// WithDescriptionTag returns a new Builder that reads field descriptions from
// the given struct tag instead of the default "description".
//
//	b := schema.NewBuilder().WithDescriptionTag("desc")
func (b *Builder) WithDescriptionTag(tag string) *Builder {
	next := b.clone()
	next.descTag = tag
	return next
}

// WithEnumTag returns a new Builder that reads enum values from the given
// struct tag instead of the default "enum".
//
//	b := schema.NewBuilder().WithEnumTag("oneof")
func (b *Builder) WithEnumTag(tag string) *Builder {
	next := b.clone()
	next.enumTag = tag
	return next
}

// clone returns a shallow copy of b with a fresh typeOverrides map.
func (b *Builder) clone() *Builder {
	overrides := make(map[reflect.Type]map[string]any, len(b.typeOverrides))
	for k, v := range b.typeOverrides {
		overrides[k] = v
	}
	return &Builder{
		typeOverrides: overrides,
		descTag:       b.descTag,
		enumTag:       b.enumTag,
	}
}

// ── Builder methods ───────────────────────────────────────────────────────────

// InputSchema derives a JSON Schema from a struct suitable for use as an LLM
// tool input schema. Pointer fields are optional (not in "required") and never
// nullable. Embedded structs are flattened, matching encoding/json behaviour.
// Nested struct fields generate recursive object schemas.
//
// Panics with a descriptive message if v is nil or not a struct / *struct.
func (b *Builder) InputSchema(v any) map[string]any {
	if v == nil {
		panic("schema.Builder.InputSchema: v must not be nil")
	}
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf(
			"schema.Builder.InputSchema: v must be a struct or *struct, got %s", t.Kind(),
		))
	}
	return b.structSchema(t)
}

// StrictTool builds a *sdkmcp.Tool and applies additionalProperties: false
// recursively to every nested object schema. Recommended for OpenAI and any
// LLM API that supports structured-output mode.
func (b *Builder) StrictTool(name, description string, args any) *sdkmcp.Tool {
	s := b.InputSchema(args)
	ApplyStrict(s)
	return &sdkmcp.Tool{Name: name, Description: description, InputSchema: s}
}

// Tool builds a *sdkmcp.Tool without strict constraints. Use for APIs that do
// not require additionalProperties to be set (e.g. Anthropic, Gemini).
func (b *Builder) Tool(name, description string, args any) *sdkmcp.Tool {
	return &sdkmcp.Tool{
		Name:        name,
		Description: description,
		InputSchema: b.InputSchema(args),
	}
}

// ── Package-level convenience (Default builder) ───────────────────────────────

// Default is the Builder used by the package-level convenience functions.
// Replace it to apply custom type overrides or tag names globally:
//
//	schema.Default = schema.NewBuilder(schema.WithTypeSchema(...))
var Default = NewBuilder()

// InputSchema is a package-level shortcut that calls Default.InputSchema.
func InputSchema(v any) map[string]any { return Default.InputSchema(v) }

// StrictTool is a package-level shortcut that calls Default.StrictTool.
func StrictTool(name, description string, args any) *sdkmcp.Tool {
	return Default.StrictTool(name, description, args)
}

// Tool is a package-level shortcut that calls Default.Tool.
func Tool(name, description string, args any) *sdkmcp.Tool {
	return Default.Tool(name, description, args)
}

// ── Strict mode ───────────────────────────────────────────────────────────────

// ApplyStrict recursively sets additionalProperties: false on every object
// schema in the tree, including objects nested inside array item schemas.
// Call this when building a schema manually and you need full OpenAI
// structured-output compliance across all nested objects.
func ApplyStrict(s map[string]any) {
	s["additionalProperties"] = false
	if props, ok := s["properties"].(map[string]any); ok {
		for _, v := range props {
			obj, ok := v.(map[string]any)
			if !ok {
				continue
			}
			switch obj["type"] {
			case "object":
				ApplyStrict(obj)
			case "array":
				if items, ok := obj["items"].(map[string]any); ok && items["type"] == "object" {
					ApplyStrict(items)
				}
			}
		}
	}
	if items, ok := s["items"].(map[string]any); ok && items["type"] == "object" {
		ApplyStrict(items)
	}
}

// ── Internal schema generation ────────────────────────────────────────────────

// structSchema generates a JSON Schema object for a struct type.
// reflect.VisibleFields flattens embedded (anonymous) structs, matching
// encoding/json behaviour.
func (b *Builder) structSchema(t reflect.Type) map[string]any {
	properties := make(map[string]any)
	var required []string

	for _, f := range reflect.VisibleFields(t) {
		if f.Anonymous {
			continue // skip the embedded type itself; promoted fields are visited individually
		}
		name, prop, isRequired, ok := b.fieldSchema(f)
		if !ok {
			continue
		}
		properties[name] = prop
		if isRequired {
			required = append(required, name)
		}
	}

	m := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

// fieldSchema extracts the JSON Schema property for a single struct field.
// Returns ok=false for fields that should be skipped (missing json tag, `json:"-",
// `json:"" with no explicit name, or unsupported type).
func (b *Builder) fieldSchema(f reflect.StructField) (name string, prop map[string]any, isRequired bool, ok bool) {
	tag := f.Tag.Get("json")
	if tag == "" || tag == "-" {
		return "", nil, false, false
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		return "", nil, false, false
	}
	omitempty := len(parts) > 1 && strings.Contains(parts[1], "omitempty")

	ft := f.Type
	isPtr := ft.Kind() == reflect.Ptr
	if isPtr {
		ft = ft.Elem()
	}

	prop = b.typeSchema(ft)
	if prop == nil {
		return "", nil, false, false
	}

	if desc := f.Tag.Get(b.descTag); desc != "" {
		prop["description"] = desc
	}
	if enumTag := f.Tag.Get(b.enumTag); enumTag != "" {
		prop["enum"] = strings.Split(enumTag, ",")
	}

	return name, prop, !isPtr && !omitempty, true
}

// typeSchema returns the JSON Schema map for a Go type, consulting type overrides
// first. Returns nil for unsupported types (channels, funcs, complex, etc.) so
// callers can skip those fields silently.
func (b *Builder) typeSchema(t reflect.Type) map[string]any {
	if override, ok := b.typeOverrides[t]; ok {
		return override
	}

	switch t.Kind() {
	case reflect.Struct:
		return b.structSchema(t)
	case reflect.Slice:
		s := map[string]any{"type": "array"}
		if items := b.typeSchema(t.Elem()); items != nil {
			s["items"] = items
		}
		return s
	case reflect.Map:
		return map[string]any{"type": "object"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	default:
		return nil
	}
}
