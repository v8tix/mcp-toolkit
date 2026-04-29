package model_test

import (
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/v8tix/mcp-toolkit/v2/model"
)

type concreteTool struct{ name string }

func (c concreteTool) Definition() *sdkmcp.Tool { return &sdkmcp.Tool{Name: c.name} }

var _ model.Tool = concreteTool{}

func TestTool_InterfaceSatisfied(t *testing.T) {
	tool := concreteTool{name: "greet"}
	def := tool.Definition()
	if def.Name != "greet" {
		t.Fatalf("expected name %q, got %q", "greet", def.Name)
	}
}
