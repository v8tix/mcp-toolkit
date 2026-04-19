// Package registry provides a thread-safe, ordered collection of LLM tools
// for use in agent dispatch loops.
package registry

import (
	"sync"

	"github.com/v8tix/mcp-toolkit/model"
)

// Registry holds a named collection of model.Tool objects for an agent loop.
//
// Storing model.Tool (the minimal interface) keeps the registry decoupled from
// the execution layer. Callers that need to execute a tool type-assert to
// handler.ExecutableTool at dispatch time:
//
//	tool, ok := reg.ByName(call.Function.Name)
//	exec, ok := tool.(handler.ExecutableTool)
//	result, err := exec.Execute(ctx, call.Function.Arguments)
//
// Registry is safe for concurrent use: reads (All, ByName, Names) hold a read
// lock; writes (Add) hold a write lock.
//
// Typical usage:
//
//	reg := registry.New(myTool)
//	req.Tools = reg.All()
//	tool, ok := reg.ByName(call.Function.Name)
type Registry struct {
	mu    sync.RWMutex
	tools []model.Tool
	index map[string]model.Tool
}

// New creates a Registry pre-populated with the given tools.
func New(t ...model.Tool) *Registry {
	r := &Registry{index: make(map[string]model.Tool)}
	return r.Add(t...)
}

// Add registers one or more tools and returns the Registry for method chaining.
// Panics if any tool's Definition().Function.Name is empty — an empty name is
// always a programmer error: the LLM API rejects tools without a name, and a
// nameless tool cannot be dispatched by ByName.
func (r *Registry) Add(t ...model.Tool) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tool := range t {
		name := tool.Definition().Function.Name
		if name == "" {
			panic("mcptoolkit.Registry.Add: tool name must not be empty")
		}
		if _, exists := r.index[name]; exists {
			for i, existing := range r.tools {
				if existing.Definition().Function.Name == name {
					r.tools[i] = tool
					break
				}
			}
		} else {
			r.tools = append(r.tools, tool)
		}
		r.index[name] = tool
	}
	return r
}

// All returns the ToolDefinitions of every registered tool in insertion order.
// Pass this slice directly to the tools field of an LLM chat-completion request.
// Returns a freshly allocated slice; mutations do not affect the registry.
func (r *Registry) All() []model.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]model.ToolDefinition, len(r.tools))
	for i, t := range r.tools {
		defs[i] = t.Definition()
	}
	return defs
}

// ByName looks up a tool by its function name and returns the model.Tool.
// Returns the tool and true if found, nil and false otherwise.
// Callers that need execution should type-assert to handler.ExecutableTool.
func (r *Registry) ByName(name string) (model.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.index[name]
	return t, ok
}

// Names returns all registered tool names in insertion order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for _, t := range r.tools {
		names = append(names, t.Definition().Function.Name)
	}
	return names
}

// Remove removes the tool with the given name from the registry.
// Returns true if a tool was removed, false if no tool had that name.
func (r *Registry) Remove(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.index[name]; !exists {
		return false
	}
	delete(r.index, name)
	for i, t := range r.tools {
		if t.Definition().Function.Name == name {
			r.tools = append(r.tools[:i], r.tools[i+1:]...)
			break
		}
	}
	return true
}

// Filter returns a new Registry containing only the tools for which fn returns
// true. The original registry is not modified.
//
//	exec := reg.Filter(func(t model.Tool) bool {
//	    _, ok := t.(handler.ExecutableTool)
//	    return ok
//	})
func (r *Registry) Filter(fn func(model.Tool) bool) *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	filtered := make([]model.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		if fn(t) {
			filtered = append(filtered, t)
		}
	}
	return New(filtered...)
}
