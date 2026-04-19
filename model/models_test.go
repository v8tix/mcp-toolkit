package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/v8tix/mcp-toolkit/model"
)

func boolPtr(b bool) *bool { return &b }

func TestInputSchema_ToMap(t *testing.T) {
	props := map[string]model.PropertySchema{
		"query": {Type: "string", Description: "Search query."},
		"limit": {Type: "integer"},
	}

	cases := []struct {
		name   string
		schema model.InputSchema
		check  func(*testing.T, map[string]any)
	}{
		{
			name: "all_fields_populated",
			schema: model.InputSchema{
				Type:                 "object",
				Properties:           props,
				Required:             []string{"query"},
				AdditionalProperties: boolPtr(false),
			},
			check: func(t *testing.T, m map[string]any) {
				assert.Equal(t, "object", m["type"])
				assert.Equal(t, props, m["properties"])
				require.Contains(t, m, "required")
				assert.Equal(t, []string{"query"}, m["required"])
				require.Contains(t, m, "additionalProperties")
				assert.Equal(t, false, m["additionalProperties"])
			},
		},
		{
			name: "empty_required_omits_key",
			schema: model.InputSchema{
				Type:       "object",
				Properties: map[string]model.PropertySchema{"x": {Type: "string"}},
			},
			check: func(t *testing.T, m map[string]any) {
				assert.NotContains(t, m, "required",
					"required key must be absent when slice is empty")
			},
		},
		{
			name: "nil_additional_properties_omits_key",
			schema: model.InputSchema{
				Type:       "object",
				Properties: map[string]model.PropertySchema{},
			},
			check: func(t *testing.T, m map[string]any) {
				assert.NotContains(t, m, "additionalProperties",
					"additionalProperties key must be absent when nil")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, tc.schema.ToMap())
		})
	}
}
