// Package observable wraps mcp-toolkit handler functions in cold rxgo Observables
// with configurable retry, exponential backoff, and error classification.
//
// Typical usage — zero config (production defaults):
//
//	tool := observable.New("search_web", "Search the web.", myFn)
//	reg.Add(tool)
//
// Tune options with fluent chaining:
//
//	tool := observable.New("search_web", "Search the web.", myFn).
//	    WithMaxRetries(5)
//
// Custom options via With (escape hatch):
//
//	tool := observable.New("search_web", "Search the web.", myFn).
//	    WithRetryPolicy(myCircuitBreaker).
//	    With(myCustomOption)
//
// The agent loop's Dispatch function detects observable.Tool and calls
// ExecuteRx for retry-aware fan-out via rxgo.Merge.
package observable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/cenkalti/backoff/v4"
	"github.com/reactivex/rxgo/v2"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/v8tix/mcp-toolkit/v2/handler"
	"github.com/v8tix/mcp-toolkit/v2/schema"
)

// ── ISP interfaces ────────────────────────────────────────────────────────────

// RetryPolicy controls retry behavior.
// Implement this interface to plug in a circuit breaker, fixed-interval
// policy, or any custom strategy.
type RetryPolicy interface {
	// MaxRetries is the maximum number of retry attempts after the first
	// failure. Zero means one attempt only (no retries).
	MaxRetries() uint64
	// NewBackOff returns a fresh backoff.BackOff instance. It is called once
	// per ExecuteRx subscription so concurrent tool calls never share state.
	NewBackOff() backoff.BackOff
}

// ErrorPolicy controls error classification.
// Return backoff.Permanent(err) to stop retrying immediately.
type ErrorPolicy interface {
	Classify(err error) error
}

// ErrorClassifier is a convenience function type that satisfies ErrorPolicy
// when wrapped with WithClassifier.
type ErrorClassifier func(err error) error

// DefaultErrorClassifier treats every error as transient (always retry up to
// MaxRetries).
func DefaultErrorClassifier(err error) error { return err }

// Permanent wraps err so the retry loop stops immediately without consuming
// any remaining retry budget. Use it in handlers or ErrorClassifiers for
// deterministic failures that retrying cannot fix (invalid input, not found,
// auth errors, etc.).
//
//	// In a handler:
//	if in.Query == "" {
//	    return "", observable.Permanent(ErrInvalidQuery)
//	}
//
//	// In a classifier:
//	observable.WithClassifier(func(err error) error {
//	    if errors.Is(err, ErrNotFound) { return observable.Permanent(err) }
//	    return err
//	})
//
// errors.Is / errors.As still work through the wrapper because
// backoff.PermanentError implements Unwrap.
func Permanent(err error) error { return backoff.Permanent(err) }

// ── Default policy implementations ───────────────────────────────────────────

// exponentialRetryPolicy is the default RetryPolicy: exponential backoff with
// a configurable retry cap.
type exponentialRetryPolicy struct{ maxRetries uint64 }

func (p exponentialRetryPolicy) MaxRetries() uint64          { return p.maxRetries }
func (p exponentialRetryPolicy) NewBackOff() backoff.BackOff { return backoff.NewExponentialBackOff() }

// classifierErrorPolicy wraps an ErrorClassifier function as an ErrorPolicy.
type classifierErrorPolicy struct{ clf ErrorClassifier }

func (p classifierErrorPolicy) Classify(err error) error { return p.clf(err) }

// ── Config + Option ───────────────────────────────────────────────────────────

// Config holds the resolved configuration for an observable Tool.
// It is intentionally exported so that callers can define their own Option
// functions that target any field — enabling extensibility without modifying
// this package:
//
//	func WithCircuitBreaker(cb RetryPolicy) observable.Option {
//	    return func(c *observable.Config) { c.Retry = cb }
//	}
type Config struct {
	// Retry controls when and how many times to retry a failed invocation.
	Retry RetryPolicy
	// ErrPolicy classifies errors before each retry attempt.
	// Return backoff.Permanent(err) to stop retrying immediately.
	ErrPolicy ErrorPolicy
	// OnRetry is called on every transient failure before the next retry.
	// attempt is 1-indexed (1 = first failure). nil = no hook.
	OnRetry func(attempt uint64, err error)
}

// Option is a functional option that configures an observable Tool.
// Options are applied left-to-right; later options override earlier ones.
type Option func(*Config)

// applyDefaults fills any nil field with the production default so callers
// that pass zero options still get sensible behaviour.
func applyDefaults(c *Config) {
	if c.Retry == nil {
		c.Retry = exponentialRetryPolicy{maxRetries: 3}
	}
	if c.ErrPolicy == nil {
		c.ErrPolicy = classifierErrorPolicy{clf: DefaultErrorClassifier}
	}
}

func buildConfig(opts []Option) Config {
	var c Config
	for _, o := range opts {
		o(&c)
	}
	applyDefaults(&c)
	return c
}

// ── Public Option constructors ────────────────────────────────────────────────

// WithRetryPolicy replaces the entire retry strategy.
//
//	tool := observable.New("t", "d", fn, observable.WithRetryPolicy(myCircuitBreaker))
func WithRetryPolicy(p RetryPolicy) Option {
	return func(c *Config) { c.Retry = p }
}

// WithErrorPolicy replaces the entire error-classification strategy.
//
//	tool := observable.New("t", "d", fn, observable.WithErrorPolicy(myPolicy))
func WithErrorPolicy(p ErrorPolicy) Option {
	return func(c *Config) { c.ErrPolicy = p }
}

// WithMaxRetries is a convenience option that sets the retry cap while keeping
// exponential backoff as the backoff strategy.
//
//	tool := observable.New("t", "d", fn, observable.WithMaxRetries(5))
func WithMaxRetries(n uint64) Option {
	return func(c *Config) { c.Retry = exponentialRetryPolicy{maxRetries: n} }
}

// WithClassifier is a convenience option that sets the error classifier while
// keeping the existing retry policy.
//
//	tool := observable.New("t", "d", fn, observable.WithClassifier(func(err error) error {
//	    if isNotFound(err) { return backoff.Permanent(err) }
//	    return err
//	}))
func WithClassifier(clf ErrorClassifier) Option {
	return func(c *Config) { c.ErrPolicy = classifierErrorPolicy{clf: clf} }
}

// WithOnRetry sets a hook that is called on every transient failure before the
// next retry attempt. attempt is 1-indexed (1 = first failure, 2 = second, …).
// The hook is not called for permanent errors since no retry will follow.
//
//	tool := observable.New("t", "d", fn,
//	    observable.WithOnRetry(func(attempt uint64, err error) {
//	        log.Printf("retry %d after: %v", attempt, err)
//	    }),
//	)
func WithOnRetry(fn func(attempt uint64, err error)) Option {
	return func(c *Config) { c.OnRetry = fn }
}

// DefaultOptions returns the production defaults as a slice so callers can
// spread and selectively override individual options:
//
//	opts := append(observable.DefaultOptions(), observable.WithMaxRetries(5))
//	tool := observable.New("t", "d", fn, opts...)
func DefaultOptions() []Option {
	return []Option{
		WithRetryPolicy(exponentialRetryPolicy{maxRetries: 3}),
		WithErrorPolicy(classifierErrorPolicy{clf: DefaultErrorClassifier}),
	}
}

// ── Tool interface ────────────────────────────────────────────────────────────

// Tool extends handler.ExecutableTool with a reactive execution path.
// Dispatch checks for this interface and calls ExecuteRx when available;
// plain ExecutableTools fall back to a simple rxgo.Defer wrapper (no retry).
type Tool interface {
	handler.ExecutableTool

	// ExecuteRx wraps Execute in a cold rxgo.Observable that applies the
	// configured retry, backoff, and error classification.
	//
	// The observable emits exactly one item on success, or terminates with
	// an error after all retries are exhausted.
	ExecuteRx(ctx context.Context, rawArgs json.RawMessage) rxgo.Observable
}

// ── observableTool — Wrap implementation ─────────────────────────────────────

// observableTool is the implementation produced by Wrap.
type observableTool struct {
	inner handler.ExecutableTool
	cfg   Config
}

var _ Tool = (*observableTool)(nil)

func (o *observableTool) Definition() *sdkmcp.Tool {
	return o.inner.Definition()
}

// Execute is the synchronous path — delegates directly to inner with no retry.
// Use ExecuteRx for the retry-aware reactive path.
func (o *observableTool) Execute(ctx context.Context, rawArgs json.RawMessage) (any, error) {
	return o.inner.Execute(ctx, rawArgs)
}

// ExecuteRx wraps Execute in a cold rxgo.Observable.
//
// rxgo.Defer keeps the call lazy: the handler is only invoked when subscribed
// (i.e. inside rxgo.Merge in Dispatch). BackOffRetry re-subscribes the Defer
// observable on each attempt, so the handler re-executes — correct semantics
// for backoff retry.
func (o *observableTool) ExecuteRx(ctx context.Context, rawArgs json.RawMessage) rxgo.Observable {
	toolName := o.inner.Definition().Name
	var attempt atomic.Uint64
	obs := rxgo.Defer([]rxgo.Producer{
		func(_ context.Context, next chan<- rxgo.Item) {
			n := attempt.Add(1)
			result, err := o.inner.Execute(ctx, rawArgs)
			if err != nil {
				// Invalid-argument errors from handler.Execute are always permanent:
				// retrying the same malformed bytes produces the same unmarshal error.
				// Wrap as Permanent before classifying so the retry budget is not wasted.
				if errors.Is(err, handler.ErrInvalidArguments) {
					next <- rxgo.Error(Permanent(err))
					return
				}
				classified := o.cfg.ErrPolicy.Classify(err)
				// Annotate transient errors with the tool name so Dispatch can
				// surface a useful message to the LLM without unwrapping the chain.
				// Permanent errors are left unchanged: they already carry the tool
				// name from the JSON-unmarshal path, and wrapping a
				// *backoff.PermanentError would break BackOffRetry's type check.
				if _, isPerm := classified.(*backoff.PermanentError); !isPerm {
					classified = fmt.Errorf("tool %q: %w", toolName, classified)
					if o.cfg.OnRetry != nil {
						o.cfg.OnRetry(n, classified)
					}
				}
				next <- rxgo.Error(classified)
				return
			}
			next <- rxgo.Of(result)
		},
	})

	maxRetries := o.cfg.Retry.MaxRetries()
	if maxRetries == 0 {
		return obs
	}
	// backoff.WithContext makes the inter-retry sleep context-aware: if ctx is
	// cancelled while the backoff is sleeping between attempts, the retry loop
	// aborts immediately instead of waiting for the full backoff duration.
	bo := backoff.WithContext(
		backoff.WithMaxRetries(o.cfg.Retry.NewBackOff(), maxRetries),
		ctx,
	)
	return obs.BackOffRetry(bo)
}

// Wrap wraps any existing handler.ExecutableTool in an observable Tool.
//
//	inner := handler.NewTool("greet", "Greet.", myFn)
//	tool  := observable.Wrap(inner, observable.WithMaxRetries(5))
func Wrap(inner handler.ExecutableTool, opts ...Option) Tool {
	return &observableTool{inner: inner, cfg: buildConfig(opts)}
}

// ── typedObservableTool — New implementation ──────────────────────────────────

// typedObservableTool is the generic implementation produced by New.
// It avoids the double-allocation of handler.NewTool + observableTool by
// holding the typed function directly.
type typedObservableTool[In any, Out any] struct {
	def *sdkmcp.Tool
	fn  func(context.Context, In) (Out, error)
	cfg Config
}

var _ Tool = (*typedObservableTool[struct{}, struct{}])(nil)

func (t *typedObservableTool[In, Out]) Definition() *sdkmcp.Tool { return t.def }

func (t *typedObservableTool[In, Out]) Execute(ctx context.Context, rawArgs json.RawMessage) (any, error) {
	var in In
	if err := json.Unmarshal(rawArgs, &in); err != nil {
		// Malformed args are a permanent failure: retrying the same bytes
		// will always produce the same unmarshal error.
		return nil, Permanent(fmt.Errorf("tool %q: invalid arguments: %w", t.def.Name, err))
	}
	out, err := t.fn(ctx, in)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (t *typedObservableTool[In, Out]) ExecuteRx(ctx context.Context, rawArgs json.RawMessage) rxgo.Observable {
	// Delegate to observableTool so retry logic stays in one place.
	return (&observableTool{inner: t, cfg: t.cfg}).ExecuteRx(ctx, rawArgs)
}

// Builder[In, Out] constructs an observable Tool from a typed handler.
// Create one with New and chain With* methods before use.
//
//	tool := observable.New("search_web", "Search.", myFn).
//	    WithMaxRetries(5).
//	    WithClassifier(myClassifier)
//
// Custom options are still supported via With:
//
//	func WithCircuitBreaker(cb RetryPolicy) observable.Option {
//	    return func(c *observable.Config) { c.Retry = cb }
//	}
//	tool := observable.New("search_web", "Search.", myFn).With(WithCircuitBreaker(cb))
type Builder[In any, Out any] struct {
	name        string
	description string
	fn          func(context.Context, In) (Out, error)
	opts        []Option

	once  sync.Once
	built Tool
}

var _ Tool = (*Builder[struct{}, struct{}])(nil)

func (b *Builder[In, Out]) resolve() Tool {
	b.once.Do(func() {
		var zero In
		b.built = &typedObservableTool[In, Out]{
			def: schema.StrictTool(b.name, b.description, zero),
			fn:  b.fn,
			cfg: buildConfig(b.opts),
		}
	})
	return b.built
}

func (b *Builder[In, Out]) clone(extra ...Option) *Builder[In, Out] {
	opts := make([]Option, len(b.opts)+len(extra))
	copy(opts, b.opts)
	copy(opts[len(b.opts):], extra)
	return &Builder[In, Out]{name: b.name, description: b.description, fn: b.fn, opts: opts}
}

// WithRetryPolicy replaces the entire retry strategy.
func (b *Builder[In, Out]) WithRetryPolicy(p RetryPolicy) *Builder[In, Out] {
	return b.clone(WithRetryPolicy(p))
}

// WithErrorPolicy replaces the entire error-classification strategy.
func (b *Builder[In, Out]) WithErrorPolicy(p ErrorPolicy) *Builder[In, Out] {
	return b.clone(WithErrorPolicy(p))
}

// WithMaxRetries sets the retry cap while keeping exponential backoff.
func (b *Builder[In, Out]) WithMaxRetries(n uint64) *Builder[In, Out] {
	return b.clone(WithMaxRetries(n))
}

// WithClassifier sets the error classifier while keeping the existing retry policy.
func (b *Builder[In, Out]) WithClassifier(clf ErrorClassifier) *Builder[In, Out] {
	return b.clone(WithClassifier(clf))
}

// WithOnRetry sets a hook called on every transient failure before next retry.
func (b *Builder[In, Out]) WithOnRetry(fn func(attempt uint64, err error)) *Builder[In, Out] {
	return b.clone(WithOnRetry(fn))
}

// With appends arbitrary Option values — the escape hatch for custom options.
//
//	tool := observable.New("t", "d", fn).With(myCustomOption)
func (b *Builder[In, Out]) With(opts ...Option) *Builder[In, Out] {
	return b.clone(opts...)
}

// Definition implements model.Tool.
func (b *Builder[In, Out]) Definition() *sdkmcp.Tool { return b.resolve().Definition() }

// Execute implements handler.ExecutableTool.
func (b *Builder[In, Out]) Execute(ctx context.Context, rawArgs json.RawMessage) (any, error) {
	return b.resolve().Execute(ctx, rawArgs)
}

// ExecuteRx implements observable.Tool.
func (b *Builder[In, Out]) ExecuteRx(ctx context.Context, rawArgs json.RawMessage) rxgo.Observable {
	return b.resolve().ExecuteRx(ctx, rawArgs)
}

// New creates a Builder for an observable Tool from a typed handler function.
// Chain With* methods to configure retry behavior before use.
//
// Zero options applies production defaults (3 retries, exponential backoff).
//
//	tool := observable.New("search_web", "Search the web.", myFn)
//	tool := observable.New("search_web", "Search the web.", myFn).WithMaxRetries(5)
func New[In any, Out any](
	name, description string,
	fn func(context.Context, In) (Out, error),
) *Builder[In, Out] {
	return &Builder[In, Out]{name: name, description: description, fn: fn}
}
