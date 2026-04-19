// Package model defines the types used to describe OpenAI-compatible
// function-calling (tool) definitions in Go.
package model

// PropertySchema describes a single parameter in a tool's JSON Schema.
// Corresponds to the per-property entries in the "properties" map.
//
// Description should always be set — it is the primary signal the model uses to
// understand what value to supply for the parameter.
// Enum should be set for any string parameter with a fixed set of valid values;
// this prevents the model from inventing values outside the allowed set.
type PropertySchema struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// InputSchema is the JSON Schema object that describes a tool's parameters.
// It maps to the "parameters" block in an OpenAI function tool definition:
//
//	{
//	  "type": "object",
//	  "properties": { "query": {"type": "string", "description": "..."}, ... },
//	  "required": ["query"],
//	  "additionalProperties": false
//	}
//
// AdditionalProperties must be false when Strict mode is enabled so the model
// cannot invent parameters outside the declared schema.
type InputSchema struct {
	Type                 string                    `json:"type"`
	Properties           map[string]PropertySchema `json:"properties"`
	Required             []string                  `json:"required,omitempty"`
	AdditionalProperties *bool                     `json:"additionalProperties,omitempty"`
}

// ToMap converts InputSchema to map[string]any without JSON serialisation.
// Use this when an SDK (e.g. openai-go) requires a generic map instead of a
// typed struct — it avoids a costly marshal/unmarshal round-trip.
func (s InputSchema) ToMap() map[string]any {
	m := map[string]any{
		"type":       s.Type,
		"properties": s.Properties,
	}
	if len(s.Required) > 0 {
		m["required"] = s.Required
	}
	if s.AdditionalProperties != nil {
		m["additionalProperties"] = *s.AdditionalProperties
	}
	return m
}

// FunctionDefinition holds the name, description, and parameter schema for an
// OpenAI function tool — the inner "function" object of a ToolDefinition.
//
// Strict enables structured-output enforcement: the model is constrained to
// produce arguments that exactly match the declared schema.
type FunctionDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  InputSchema `json:"parameters"`
	Strict      bool        `json:"strict,omitempty"`
}

// ToolDefinition is the top-level OpenAI tool schema:
//
//	{"type": "function", "function": { "name": ..., "description": ..., "parameters": ... }}
type ToolDefinition struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

// Tool is the interface every concrete tool must satisfy.
//
// By making each tool a type (rather than a plain function), the tool becomes
// a first-class object that can:
//   - carry its own dependencies (injected at construction time)
//   - be polymorphically stored in a Registry
//   - be dispatched by name without a separate lookup table
//
// A compile-time guard is recommended in each implementation:
//
//	var _ model.Tool = MyTool{}
type Tool interface {
	Definition() ToolDefinition
}
