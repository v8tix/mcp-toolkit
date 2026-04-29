package mcp

import (
	"context"
	"encoding/json"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/v8tix/mcp-toolkit/v2/handler"
	"github.com/v8tix/mcp-toolkit/v2/model"
	"github.com/v8tix/mcp-toolkit/v2/registry"
)

// RegisterTools registers every tool in reg on s.
// Each tool's Execute result is JSON-marshaled into a single text content item.
// Execution errors are returned as tool errors (IsError=true), not protocol errors.
//
// Pass one or more filter functions to selectively expose tools — a tool is
// registered only when all filters return true for it:
//
//	mcp.RegisterTools(s, reg, func(t model.Tool) bool {
//	    return t.Definition().Name != "internal_tool"
//	})
func RegisterTools(s *sdkmcp.Server, reg *registry.Registry, filters ...func(model.Tool) bool) {
	for _, name := range reg.Names() {
		t, ok := reg.ByName(name)
		if !ok {
			continue
		}

		skip := false
		for _, f := range filters {
			if !f(t) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		exec, ok := t.(handler.ExecutableTool)
		if !ok {
			continue
		}

		execTool := exec

		s.AddTool(
			t.Definition(),
			func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				result, err := execTool.Execute(ctx, req.Params.Arguments)
				if err != nil {
					res := &sdkmcp.CallToolResult{}
					res.SetError(err)
					return res, nil
				}

				b, _ := json.Marshal(result)
				return &sdkmcp.CallToolResult{
					Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(b)}},
				}, nil
			},
		)
	}
}
