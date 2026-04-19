// Package mcptoolkit provides types and helpers for defining OpenAI-compatible
// function-calling (tool) schemas in Go.
//
// Sub-packages:
//   - github.com/v8tix/mcp-toolkit/model    — schema structs and Tool interface
//   - github.com/v8tix/mcp-toolkit/schema   — schema builder functions
//   - github.com/v8tix/mcp-toolkit/handler  — typed execution wrappers (NewTool, Wrap)
//   - github.com/v8tix/mcp-toolkit/registry — thread-safe tool Registry
//   - github.com/v8tix/mcp-toolkit/observable — retry-aware execution with backoff
//   - github.com/v8tix/mcp-toolkit/mcp      — MCP client/server bridge
package mcptoolkit
