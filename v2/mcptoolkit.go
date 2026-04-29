// Package mcptoolkit provides types and helpers for defining MCP-compatible
// tool schemas in Go, backed by the official MCP Go SDK.
//
// Sub-packages:
//   - github.com/v8tix/mcp-toolkit/v2/model    — Tool interface (returns *sdkmcp.Tool)
//   - github.com/v8tix/mcp-toolkit/v2/schema   — schema builder functions
//   - github.com/v8tix/mcp-toolkit/v2/handler  — typed execution wrappers (NewTool, Wrap)
//   - github.com/v8tix/mcp-toolkit/v2/registry — thread-safe tool Registry
//   - github.com/v8tix/mcp-toolkit/v2/observable — retry-aware execution with backoff
//   - github.com/v8tix/mcp-toolkit/v2/mcp      — MCP client/server bridge
package mcptoolkit
