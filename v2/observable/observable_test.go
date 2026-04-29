package observable_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/v8tix/mcp-toolkit/v2/handler"
	"github.com/v8tix/mcp-toolkit/v2/observable"
	"github.com/v8tix/mcp-toolkit/v2/schema"
)

// ── helpers ───────────────────────────────────────────────────────────────────

type greetArgs struct {
	Name string `json:"name" description:"Name to greet."`
}

func greetFn(_ context.Context, in greetArgs) (string, error) {
	return "Hello, " + in.Name, nil
}

func greetInner() handler.ExecutableTool {
	return handler.NewTool("greet", "Greet someone.", greetFn)
}

// ── DefaultErrorClassifier ────────────────────────────────────────────────────

func TestDefaultErrorClassifier_ReturnsSameError(t *testing.T) {
	sentinel := errors.New("boom")
	got := observable.DefaultErrorClassifier(sentinel)
	assert.Equal(t, sentinel, got)
}

// ── DefaultOptions ────────────────────────────────────────────────────────────

func TestDefaultOptions_AppliesDefaults(t *testing.T) {
	// New with no options should apply defaults (3 retries, no panic)
	tool := observable.New("greet", "Greet someone.", greetFn)
	result, err := tool.Execute(context.Background(), []byte(`{"name":"World"}`))
	require.NoError(t, err)
	assert.Equal(t, "Hello, World", result)
}

func TestDefaultOptions_HelperReturnsNonEmpty(t *testing.T) {
	opts := observable.DefaultOptions()
	assert.NotEmpty(t, opts)
}

// ── New / Wrap parity ─────────────────────────────────────────────────────────

func TestObservableTool_DefinitionParity(t *testing.T) {
	want := schema.StrictTool("greet", "Greet someone.", greetArgs{})
	cases := []struct {
		name string
		tool observable.Tool
	}{
		{"New", observable.New("greet", "Greet someone.", greetFn)},
		{"Wrap", observable.Wrap(greetInner())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, want, tc.tool.Definition())
		})
	}
}

func TestObservableTool_ExecuteParity(t *testing.T) {
	cases := []struct {
		name string
		tool observable.Tool
	}{
		{"New", observable.New("greet", "Greet someone.", greetFn)},
		{"Wrap", observable.Wrap(greetInner())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.tool.Execute(context.Background(), []byte(`{"name":"Alice"}`))
			require.NoError(t, err)
			assert.Equal(t, "Hello, Alice", result)
		})
	}
}

func TestWrap_WithMaxRetries_OverridesDefault(t *testing.T) {
	var calls atomic.Int32
	sentinel := errors.New("transient")
	inner := handler.NewTool("t", "t",
		func(_ context.Context, in greetArgs) (string, error) {
			n := calls.Add(1)
			if n < 2 {
				return "", sentinel
			}
			return "ok", nil
		})
	tool := observable.Wrap(inner, observable.WithRetryPolicy(fixedRetryPolicy{max: 5}))

	var gotResult any
	for item := range tool.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
		if item.V != nil {
			gotResult = item.V
		}
	}
	assert.Equal(t, "ok", gotResult)
	assert.Equal(t, int32(2), calls.Load())
}

// ── WithRetryPolicy ───────────────────────────────────────────────────────────

type fixedRetryPolicy struct{ max uint64 }

func (p fixedRetryPolicy) MaxRetries() uint64          { return p.max }
func (p fixedRetryPolicy) NewBackOff() backoff.BackOff { return backoff.NewConstantBackOff(0) }

func TestWithRetryPolicy_CustomPolicyUsed(t *testing.T) {
	var calls atomic.Int32
	sentinel := errors.New("transient")

	fn := func(_ context.Context, in greetArgs) (string, error) {
		n := calls.Add(1)
		if n < 3 {
			return "", sentinel
		}
		return "ok", nil
	}

	tool := observable.New("t", "t", fn).WithRetryPolicy(fixedRetryPolicy{max: 5})

	var gotResult any
	for item := range tool.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
		if item.V != nil {
			gotResult = item.V
		}
	}
	assert.Equal(t, "ok", gotResult)
	assert.Equal(t, int32(3), calls.Load())
}

// ── WithErrorPolicy ───────────────────────────────────────────────────────────

type permanentPolicy struct{}

func (permanentPolicy) Classify(err error) error { return backoff.Permanent(err) }

func TestWithErrorPolicy_PermanentPolicyStopsRetry(t *testing.T) {
	var calls atomic.Int32

	fn := func(_ context.Context, _ greetArgs) (string, error) {
		calls.Add(1)
		return "", errors.New("not found")
	}

	tool := observable.New("t", "t", fn).
		WithRetryPolicy(fixedRetryPolicy{max: 5}).
		WithErrorPolicy(permanentPolicy{})

	var lastErr error
	for item := range tool.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
		if item.E != nil {
			lastErr = item.E
		}
	}

	assert.Error(t, lastErr)
	assert.Equal(t, int32(1), calls.Load(), "permanent policy must stop after one attempt")
}

// ── WithClassifier (convenience) ──────────────────────────────────────────────

func TestWithClassifier_PermanentErrorStopsImmediately(t *testing.T) {
	sentinel := errors.New("not found")
	var calls atomic.Int32

	fn := func(_ context.Context, _ greetArgs) (string, error) {
		calls.Add(1)
		return "", sentinel
	}

	tool := observable.New("t", "t", fn).
		WithRetryPolicy(fixedRetryPolicy{max: 5}).
		WithClassifier(func(err error) error { return backoff.Permanent(err) })

	var lastErr error
	for item := range tool.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
		if item.E != nil {
			lastErr = item.E
		}
	}

	assert.Error(t, lastErr)
	assert.Equal(t, int32(1), calls.Load(), "permanent classifier must not retry")
}

// ── ExecuteRx — happy path ────────────────────────────────────────────────────

func TestExecuteRx_SuccessEmitsOneItem(t *testing.T) {
	tool := observable.New("greet", "Greet someone.", greetFn)
	obs := tool.ExecuteRx(context.Background(), []byte(`{"name":"Bob"}`))

	var items []any
	for item := range obs.Observe() {
		require.NoError(t, item.E)
		items = append(items, item.V)
	}
	require.Len(t, items, 1)
	assert.Equal(t, "Hello, Bob", items[0])
}

func TestExecuteRx_ZeroMaxRetries_TriesOnce(t *testing.T) {
	var calls atomic.Int32
	fn := func(_ context.Context, in greetArgs) (string, error) {
		calls.Add(1)
		return "hi", nil
	}
	tool := observable.New("t", "t", fn).WithMaxRetries(0)

	for range tool.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
	}
	assert.Equal(t, int32(1), calls.Load())
}

// ── ExecuteRx — retry ─────────────────────────────────────────────────────────

func TestExecuteRx_TransientError_RetriesAndSucceeds(t *testing.T) {
	var calls atomic.Int32
	sentinel := errors.New("transient")

	fn := func(_ context.Context, in greetArgs) (string, error) {
		n := calls.Add(1)
		if n < 3 {
			return "", sentinel
		}
		return "Hello, " + in.Name, nil
	}

	tool := observable.New("greet", "Greet.", fn).WithRetryPolicy(fixedRetryPolicy{max: 5})

	var gotResult any
	var gotErr error
	for item := range tool.ExecuteRx(context.Background(), []byte(`{"name":"Retry"}`)).Observe() {
		if item.E != nil {
			gotErr = item.E
		} else {
			gotResult = item.V
		}
	}

	require.NoError(t, gotErr)
	assert.Equal(t, "Hello, Retry", gotResult)
	assert.Equal(t, int32(3), calls.Load(), "handler must be called exactly 3 times")
}

func TestExecuteRx_ExhaustedRetries_EmitsError(t *testing.T) {
	sentinel := errors.New("always fails")
	var calls atomic.Int32

	fn := func(_ context.Context, _ greetArgs) (string, error) {
		calls.Add(1)
		return "", sentinel
	}

	tool := observable.New("t", "t", fn).WithRetryPolicy(fixedRetryPolicy{max: 2})

	var lastErr error
	for item := range tool.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
		if item.E != nil {
			lastErr = item.E
		}
	}

	assert.Error(t, lastErr)
	// MaxRetries=2: 1 initial attempt + 2 retries = 3 total calls
	assert.Equal(t, int32(3), calls.Load())
}

// ── Extensibility: external package can define custom Options ─────────────────

// withFixedRetries is a custom Option defined *outside* the observable package
// (here in the _test package). It is only possible because observable.Config
// and its fields are exported — proving that third-party callers can extend the
// option set without modifying the library.
func withFixedRetries(n uint64) observable.Option {
	return func(c *observable.Config) {
		c.Retry = fixedRetryPolicy{max: n}
	}
}

func TestCustomOption_ExternalPackageCanDefineNewOption(t *testing.T) {
	var calls atomic.Int32
	fn := func(_ context.Context, _ greetArgs) (string, error) {
		calls.Add(1)
		return "", errors.New("always fails")
	}

	// withFixedRetries is defined in this test file, not in the observable
	// package — yet it compiles and works because Config is exported.
	tool := observable.New("t", "t", fn).With(withFixedRetries(2))

	for range tool.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
	}
	// fixedRetryPolicy maxRetries=2 → 1 initial + 2 retries = 3 total calls
	assert.Equal(t, int32(3), calls.Load(), "custom external option must be applied")
}

// ── Option composition ────────────────────────────────────────────────────────

func TestOptionsComposition_LaterOptionOverridesEarlier(t *testing.T) {
	// Apply defaults then override MaxRetries — should use the later value (1)
	var calls atomic.Int32
	fn := func(_ context.Context, _ greetArgs) (string, error) {
		calls.Add(1)
		return "", errors.New("always fails")
	}

	opts := append(observable.DefaultOptions(), observable.WithRetryPolicy(fixedRetryPolicy{max: 1}))
	tool := observable.New("t", "t", fn).With(opts...)

	for range tool.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
	}
	// MaxRetries=1: 1 initial + 1 retry = 2 total calls
	assert.Equal(t, int32(2), calls.Load(), "override must win over default")
}

// ── Permanent ─────────────────────────────────────────────────────────────────

func TestPermanent(t *testing.T) {
	sentinel := errors.New("not found")
	wrapped := observable.Permanent(sentinel)

	t.Run("preserves_error_message", func(t *testing.T) {
		assert.Error(t, wrapped)
		assert.Equal(t, sentinel.Error(), wrapped.Error(),
			"Permanent must preserve the original error message")
	})

	t.Run("errors_is_unwraps_through_wrapper", func(t *testing.T) {
		assert.True(t, errors.Is(wrapped, sentinel),
			"errors.Is must unwrap through Permanent")
	})
}

func TestPermanent_InHandlerStopsRetry(t *testing.T) {
	sentinel := errors.New("invalid input")
	var calls atomic.Int32

	fn := func(_ context.Context, _ greetArgs) (string, error) {
		calls.Add(1)
		return "", observable.Permanent(sentinel)
	}

	tool := observable.New("t", "t", fn).WithRetryPolicy(fixedRetryPolicy{max: 5})

	var lastErr error
	for item := range tool.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
		if item.E != nil {
			lastErr = item.E
		}
	}

	assert.Error(t, lastErr)
	assert.True(t, errors.Is(lastErr, sentinel), "errors.Is must work on the emitted error")
	assert.Equal(t, int32(1), calls.Load(), "Permanent in handler must stop after one attempt")
}

func TestPermanent_InvalidJSON_NeverRetried(t *testing.T) {
	// JSON unmarshal errors are permanently wrapped inside typedObservableTool.Execute
	// so they must not consume the retry budget regardless of MaxRetries.
	var calls atomic.Int32
	fn := func(_ context.Context, _ greetArgs) (string, error) {
		calls.Add(1)
		return "ok", nil
	}

	tool := observable.New("t", "t", fn).WithRetryPolicy(fixedRetryPolicy{max: 5})

	var lastErr error
	for item := range tool.ExecuteRx(context.Background(), []byte(`not-valid-json`)).Observe() {
		if item.E != nil {
			lastErr = item.E
		}
	}

	assert.Error(t, lastErr)
	assert.Contains(t, lastErr.Error(), `tool "t"`, "error must name the tool")
	assert.Equal(t, int32(0), calls.Load(),
		"handler must never be called when JSON unmarshal fails")
}

func TestWrap_InvalidJSON_NeverRetried(t *testing.T) {
	// Wrap delegates to handler.Execute which returns handler.ErrInvalidArguments
	// for malformed JSON. observableTool.ExecuteRx must promote this to Permanent
	// so the retry budget is not consumed — matching New's behavior.
	var calls atomic.Int32
	inner := handler.NewTool("wrapped", "Wrapped.",
		func(_ context.Context, _ greetArgs) (string, error) {
			calls.Add(1)
			return "ok", nil
		},
	)
	tool := observable.Wrap(inner, observable.WithRetryPolicy(fixedRetryPolicy{max: 5}))

	var lastErr error
	for item := range tool.ExecuteRx(context.Background(), []byte(`not-valid-json`)).Observe() {
		if item.E != nil {
			lastErr = item.E
		}
	}

	assert.Error(t, lastErr)
	assert.True(t, errors.Is(lastErr, handler.ErrInvalidArguments),
		"error must wrap ErrInvalidArguments for diagnostic classification")
	assert.Equal(t, int32(0), calls.Load(),
		"Wrap must not retry on invalid JSON — matching New behavior")
}

// ── ExecuteRx — cold observable semantics ────────────────────────────────────

func TestExecuteRx_ColdObservable_SubscribeTwiceRunsHandlerTwice(t *testing.T) {
	// Each ExecuteRx call returns an independent observable. Two calls on the
	// same tool must each invoke the handler — verifying that no shared state
	// (channels, producers) leaks between invocations.
	var calls atomic.Int32
	fn := func(_ context.Context, _ greetArgs) (string, error) {
		calls.Add(1)
		return "ok", nil
	}

	tool := observable.New("t", "t", fn)

	for range tool.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
	}
	for range tool.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
	}

	assert.Equal(t, int32(2), calls.Load(),
		"each ExecuteRx call must produce an independent execution of the handler")
}

// ── ExecuteRx — concurrent independence ──────────────────────────────────────

func TestExecuteRx_ConcurrentCalls_IndependentBackoff(t *testing.T) {
	// Each ExecuteRx invocation gets its own backoff state via NewBackOff().
	// Two concurrent calls on the same tool must not share retry counters.
	var callsA, callsB atomic.Int32

	fnA := func(_ context.Context, _ greetArgs) (string, error) {
		n := callsA.Add(1)
		if n < 3 {
			return "", errors.New("transient A")
		}
		return "A", nil
	}
	fnB := func(_ context.Context, _ greetArgs) (string, error) {
		n := callsB.Add(1)
		if n < 2 {
			return "", errors.New("transient B")
		}
		return "B", nil
	}

	toolA := observable.New("a", "a", fnA).WithRetryPolicy(fixedRetryPolicy{max: 5})
	toolB := observable.New("b", "b", fnB).WithRetryPolicy(fixedRetryPolicy{max: 5})

	type result struct {
		val any
		err error
	}
	chA := make(chan result, 1)
	chB := make(chan result, 1)

	go func() {
		var r result
		for item := range toolA.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
			if item.E != nil {
				r.err = item.E
			} else {
				r.val = item.V
			}
		}
		chA <- r
	}()
	go func() {
		var r result
		for item := range toolB.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
			if item.E != nil {
				r.err = item.E
			} else {
				r.val = item.V
			}
		}
		chB <- r
	}()

	rA := <-chA
	rB := <-chB

	require.NoError(t, rA.err)
	require.NoError(t, rB.err)
	assert.Equal(t, "A", rA.val)
	assert.Equal(t, "B", rB.val)
	assert.Equal(t, int32(3), callsA.Load(), "toolA must have retried independently")
	assert.Equal(t, int32(2), callsB.Load(), "toolB must have retried independently")
}

// ── ExecuteRx — errors.Is after exhaustion ───────────────────────────────────

func TestExecuteRx_ErrorsIs_AfterExhaustion(t *testing.T) {
	sentinel := errors.New("upstream down")
	fn := func(_ context.Context, _ greetArgs) (string, error) {
		return "", sentinel
	}

	tool := observable.New("t", "t", fn).WithRetryPolicy(fixedRetryPolicy{max: 2})

	var lastErr error
	for item := range tool.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
		if item.E != nil {
			lastErr = item.E
		}
	}

	require.Error(t, lastErr)
	assert.True(t, errors.Is(lastErr, sentinel),
		"errors.Is must reach the original sentinel after retry exhaustion")
}

// ── Wrap / New parity ─────────────────────────────────────────────────────────

func TestWrap_RetryCountMatchesNew(t *testing.T) {
	// Both Wrap and New must apply identical retry logic for the same options.
	var cntNew, cntWrap atomic.Int32

	fnNew := func(_ context.Context, _ greetArgs) (string, error) {
		cntNew.Add(1)
		return "", errors.New("always fails")
	}
	fnWrap := func(_ context.Context, _ greetArgs) (string, error) {
		cntWrap.Add(1)
		return "", errors.New("always fails")
	}

	toolNew := observable.New("t", "t", fnNew).WithRetryPolicy(fixedRetryPolicy{max: 2})
	toolWrap := observable.Wrap(
		handler.NewTool("t", "t", fnWrap),
		observable.WithRetryPolicy(fixedRetryPolicy{max: 2}),
	)

	for range toolNew.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
	}
	for range toolWrap.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
	}

	assert.Equal(t, cntNew.Load(), cntWrap.Load(),
		"New and Wrap must produce the same retry count for identical options")
}

// ── ExecuteRx — tool name in error ────────────────────────────────────────────

// TestExecuteRx_ErrorContainsToolName verifies that handler errors emitted by
// ExecuteRx (after retry exhaustion) carry the tool name for diagnostic context,
// and that errors.Is still works through the wrapping.
func TestExecuteRx_ErrorContainsToolName(t *testing.T) {
	sentinel := errors.New("upstream unavailable")

	cases := []struct {
		name string
		tool observable.Tool
	}{
		{
			name: "New",
			tool: observable.New("my_tool", "My tool.",
				func(_ context.Context, _ greetArgs) (string, error) { return "", sentinel },
			).WithRetryPolicy(fixedRetryPolicy{max: 1}),
		},
		{
			name: "Wrap",
			tool: observable.Wrap(
				handler.NewTool("my_wrapped_tool", "My tool.",
					func(_ context.Context, in greetArgs) (string, error) { return "", sentinel },
				),
				observable.WithRetryPolicy(fixedRetryPolicy{max: 1}),
			),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var lastErr error
			for item := range tc.tool.ExecuteRx(context.Background(), []byte(`{"name":"x"}`)).Observe() {
				if item.E != nil {
					lastErr = item.E
				}
			}
			require.Error(t, lastErr)
			assert.Contains(t, lastErr.Error(), "tool ",
				"error message must contain 'tool <name>' for diagnostic context")
			assert.True(t, errors.Is(lastErr, sentinel),
				"errors.Is must unwrap through the tool name wrapper")
		})
	}
}

// ── Nil policy safety ─────────────────────────────────────────────────────────

func TestWithRetryPolicy_Nil_AppliesDefault(t *testing.T) {
	// Explicitly passing nil must not panic — applyDefaults fills the gap.
	fn := func(_ context.Context, _ greetArgs) (string, error) {
		return "ok", nil
	}
	assert.NotPanics(t, func() {
		tool := observable.New("t", "t", fn).WithRetryPolicy(nil)
		result, err := tool.Execute(context.Background(), []byte(`{"name":"x"}`))
		assert.NoError(t, err)
		assert.Equal(t, "ok", result)
	})
}

func TestWithErrorPolicy_Nil_AppliesDefault(t *testing.T) {
	fn := func(_ context.Context, _ greetArgs) (string, error) {
		return "ok", nil
	}
	assert.NotPanics(t, func() {
		tool := observable.New("t", "t", fn).WithErrorPolicy(nil)
		result, err := tool.Execute(context.Background(), []byte(`{"name":"x"}`))
		assert.NoError(t, err)
		assert.Equal(t, "ok", result)
	})
}

// ── Context cancellation ──────────────────────────────────────────────────────

// TestExecuteRx_ContextCancellation_StopsRetry verifies that cancelling the
// context while the tool is mid-retry causes the observable to stop and the
// emitted error to wrap context.Canceled.
func TestExecuteRx_ContextCancellation_StopsRetry(t *testing.T) {
	// The handler blocks until its context is cancelled, then surfaces the
	// cancellation error.  Without context propagation the observable would
	// spin on retries indefinitely.
	ready := make(chan struct{})
	fn := func(ctx context.Context, _ greetArgs) (string, error) {
		select {
		case ready <- struct{}{}: // signal that first call started
		default:
		}
		<-ctx.Done()
		return "", ctx.Err()
	}

	tool := observable.New("t", "t", fn).
		WithRetryPolicy(fixedRetryPolicy{max: 10}) // large budget — cancellation must cut it short

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		var lastErr error
		for item := range tool.ExecuteRx(ctx, []byte(`{"name":"x"}`)).Observe() {
			if item.E != nil {
				lastErr = item.E
			}
		}
		done <- lastErr
	}()

	// Wait until the handler has been entered at least once, then cancel.
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never started")
	}
	cancel()

	select {
	case lastErr := <-done:
		require.Error(t, lastErr)
		assert.True(t, errors.Is(lastErr, context.Canceled),
			"expected context.Canceled in error chain, got: %v", lastErr)
	case <-time.After(5 * time.Second):
		t.Fatal("observable did not terminate after context cancellation")
	}
}

// ── WithOnRetry ───────────────────────────────────────────────────────────────

func drainRx(ctx context.Context, tool observable.Tool, rawArgs []byte) (any, error) {
	var result any
	var execErr error
	for item := range tool.ExecuteRx(ctx, rawArgs).Observe() {
		if item.E != nil {
			execErr = item.E
		} else {
			result = item.V
		}
	}
	return result, execErr
}

func TestWithOnRetry_CalledOnTransientFailure(t *testing.T) {
	sentinel := errors.New("transient")
	var calls atomic.Int32
	var hookedAttempts []uint64

	tool := observable.New("t", "d",
		func(_ context.Context, _ greetArgs) (string, error) {
			n := calls.Add(1)
			if n <= 2 {
				return "", sentinel
			}
			return "ok", nil
		},
	).
		WithRetryPolicy(fixedRetryPolicy{max: 3}).
		WithOnRetry(func(attempt uint64, _ error) {
			hookedAttempts = append(hookedAttempts, attempt)
		})

	result, err := drainRx(context.Background(), tool, []byte(`{"name":"x"}`))
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, []uint64{1, 2}, hookedAttempts,
		"hook called once per transient failure, 1-indexed")
}

func TestWithOnRetry_NotCalledOnPermanentError(t *testing.T) {
	hookCalled := false
	tool := observable.New("t", "d",
		func(_ context.Context, _ greetArgs) (string, error) {
			return "", observable.Permanent(errors.New("perm"))
		},
	).
		WithRetryPolicy(fixedRetryPolicy{max: 3}).
		WithOnRetry(func(_ uint64, _ error) { hookCalled = true })

	_, err := drainRx(context.Background(), tool, []byte(`{"name":"x"}`))
	require.Error(t, err)
	assert.False(t, hookCalled, "hook must not be called for permanent errors")
}

func TestWithOnRetry_NotCalledOnSuccess(t *testing.T) {
	hookCalled := false
	tool := observable.New("t", "d", greetFn).
		WithRetryPolicy(fixedRetryPolicy{max: 3}).
		WithOnRetry(func(_ uint64, _ error) { hookCalled = true })

	_, err := drainRx(context.Background(), tool, []byte(`{"name":"World"}`))
	require.NoError(t, err)
	assert.False(t, hookCalled)
}

func TestWithOnRetry_AttemptNumberIsOneIndexed(t *testing.T) {
	var firstAttempt uint64
	var calls atomic.Int32
	tool := observable.New("t", "d",
		func(_ context.Context, _ greetArgs) (string, error) {
			n := calls.Add(1)
			if n == 1 {
				return "", errors.New("first fail")
			}
			return "ok", nil
		},
	).
		WithRetryPolicy(fixedRetryPolicy{max: 1}).
		WithOnRetry(func(attempt uint64, _ error) {
			firstAttempt = attempt
		})

	_, err := drainRx(context.Background(), tool, []byte(`{"name":"x"}`))
	require.NoError(t, err)
	assert.Equal(t, uint64(1), firstAttempt, "first failure = attempt 1")
}
