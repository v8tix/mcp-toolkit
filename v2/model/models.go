// Package model defines the Tool interface used throughout mcp-toolkit.
package model

import sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

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
	Definition() *sdkmcp.Tool
}
