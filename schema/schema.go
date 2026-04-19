// Package schema provides schema-builder functions for constructing
// OpenAI-compatible function-calling (tool) definitions in Go.
// It builds on the types defined in github.com/v8tix/mcp-toolkit/model.
package schema

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/v8tix/mcp-toolkit/model"
)

// JSONSchemaKinds maps Go reflect.Kind values to their JSON Schema type strings.
// Exported so callers (tests, validators, custom schema builders) can reuse the
// same mapping without duplicating it.
var JSONSchemaKinds = map[reflect.Kind]string{
	reflect.String:  "string",
	reflect.Int:     "integer",
	reflect.Int8:    "integer",
	reflect.Int16:   "integer",
	reflect.Int32:   "integer",
	reflect.Int64:   "integer",
	reflect.Float32: "number",
	reflect.Float64: "number",
	reflect.Bool:    "boolean",
	reflect.Slice:   "array",
	reflect.Map:     "object",
	reflect.Struct:  "object",
}

// NewInputSchemaFromStruct derives an InputSchema from a struct's json, desc,
// and enum tags using reflection. This makes the struct the single source of truth:
// adding or removing a field automatically updates the tool definition schema.
//
// Struct tag conventions:
//   - `json:"name"`           → property name (required)
//   - `json:"name,omitempty"` → optional parameter
//   - `desc:"..."`            → description shown to the model — always provide one
//   - `enum:"a,b,c"`          → restricts a string parameter to the listed values
//
// Required/optional rules (mirrors json serialisation conventions):
//   - Non-pointer field without omitempty → added to "required"
//   - Non-pointer field with omitempty    → optional (has a default)
//   - Pointer field                       → optional (may be absent)
//
// Panics with a descriptive message if v is nil or not a struct / *struct.
func NewInputSchemaFromStruct(v any) model.InputSchema {
	if v == nil {
		panic("mcptoolkit.NewInputSchemaFromStruct: v must not be nil")
	}
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf(
			"mcptoolkit.NewInputSchemaFromStruct: v must be a struct or *struct, got %s", t.Kind(),
		))
	}

	properties := make(map[string]model.PropertySchema)
	var required []string

	for i := range t.NumField() {
		name, prop, isRequired, ok := buildFieldProperty(t.Field(i))
		if !ok {
			continue
		}
		properties[name] = prop
		if isRequired {
			required = append(required, name)
		}
	}

	return model.InputSchema{
		Type:       "object",
		Properties: properties,
		Required:   required,
	}
}

// buildFieldProperty extracts the JSON schema property name, schema, and
// required flag from a single struct field. Returns ok=false for fields that
// should be skipped (no json tag, or explicitly excluded with json:"-").
func buildFieldProperty(field reflect.StructField) (name string, prop model.PropertySchema, isRequired bool, ok bool) {
	tag := field.Tag.Get("json")
	if tag == "" || tag == "-" {
		return "", model.PropertySchema{}, false, false
	}

	parts := strings.Split(tag, ",")
	name = parts[0]
	omitempty := len(parts) > 1 && strings.Contains(parts[1], "omitempty")

	kind := field.Type.Kind()
	isPtr := kind == reflect.Ptr
	if isPtr {
		kind = field.Type.Elem().Kind()
	}

	jsonType, found := JSONSchemaKinds[kind]
	if !found {
		jsonType = "string"
	}

	var enumVals []string
	if enumTag := field.Tag.Get("enum"); enumTag != "" {
		enumVals = strings.Split(enumTag, ",")
	}

	return name, model.PropertySchema{
		Type:        jsonType,
		Description: field.Tag.Get("desc"),
		Enum:        enumVals,
	}, !isPtr && !omitempty, true
}

// FormatToolDefinition builds a ToolDefinition from explicit name, description,
// and parameter schema. When strict is true, additionalProperties is set to false
// and Strict is enabled — both required by the OpenAI structured-output spec.
func FormatToolDefinition(name, description string, params model.InputSchema, strict bool) model.ToolDefinition {
	if strict {
		params.AdditionalProperties = new(false)
	}
	return model.ToolDefinition{
		Type: "function",
		Function: model.FunctionDefinition{
			Name:        name,
			Description: description,
			Parameters:  params,
			Strict:      strict,
		},
	}
}

// NewStrictTool is the primary convenience constructor for defining a tool.
// It combines NewInputSchemaFromStruct and FormatToolDefinition into a single
// call and always enables strict mode, which is the recommended setting.
//
// args must be a struct (or pointer to struct) whose fields are annotated with
// json, desc, and enum tags — see NewInputSchemaFromStruct for the full convention.
func NewStrictTool(name, description string, args any) model.ToolDefinition {
	return FormatToolDefinition(name, description, NewInputSchemaFromStruct(args), true)
}
