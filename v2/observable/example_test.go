package observable_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/v8tix/mcp-toolkit/v2/handler"
	"github.com/v8tix/mcp-toolkit/v2/observable"
)

type calcArgs struct {
	A int `json:"a" description:"First operand."`
	B int `json:"b" description:"Second operand."`
}

func ExampleNew() {
	tool := observable.New("add", "Add two integers.",
		func(_ context.Context, in calcArgs) (int, error) {
			return in.A + in.B, nil
		},
	)

	fmt.Println(tool.Definition().Name)
	// Output:
	// add
}

func ExampleWrap() {
	base := handler.NewTool("add", "Add two integers.",
		func(_ context.Context, in calcArgs) (int, error) {
			return in.A + in.B, nil
		},
	)

	tool := observable.Wrap(base, observable.WithMaxRetries(5))
	fmt.Println(tool.Definition().Name)
	// Output:
	// add
}

func ExampleWithMaxRetries() {
	tool := observable.New("add", "Add.",
		func(_ context.Context, in calcArgs) (int, error) { return in.A + in.B, nil },
	).WithMaxRetries(0) // no retry

	fmt.Println(tool.Definition().Name)
	// Output:
	// add
}

func ExampleWithOnRetry() {
	attempts := 0
	calls := 0

	tool := observable.New("flaky", "Flaky tool.",
		func(_ context.Context, _ calcArgs) (int, error) {
			calls++
			if calls < 3 {
				return 0, errors.New("transient")
			}
			return 42, nil
		},
	).
		WithMaxRetries(5).
		WithOnRetry(func(attempt uint64, err error) {
			attempts++
			log.Printf("retry %d: %v", attempt, err)
		})

	var result any
	for item := range tool.ExecuteRx(context.Background(), json.RawMessage(`{"a":1,"b":2}`)).Observe() {
		if item.E == nil {
			result = item.V
		}
	}

	fmt.Println(result)
	fmt.Println(attempts)
	// Output:
	// 42
	// 2
}

func ExamplePermanent() {
	errNotFound := errors.New("not found")

	tool := observable.New("find", "Find by ID.",
		func(_ context.Context, in struct {
			ID int `json:"id"`
		}) (string, error) {
			if in.ID == 0 {
				// Stop retrying immediately — no point retrying a missing ID.
				return "", observable.Permanent(errNotFound)
			}
			return "found", nil
		},
	).WithMaxRetries(5)

	var execErr error
	for item := range tool.ExecuteRx(context.Background(), json.RawMessage(`{"id":0}`)).Observe() {
		if item.E != nil {
			execErr = item.E
		}
	}

	fmt.Println(errors.Is(execErr, errNotFound))
	// Output:
	// true
}

func ExampleWithClassifier() {
	errBadInput := errors.New("bad input")

	tool := observable.New("process", "Process.",
		func(_ context.Context, _ struct{}) (string, error) {
			return "", errBadInput
		},
	).
		WithMaxRetries(5).
		WithClassifier(func(err error) error {
			if errors.Is(err, errBadInput) {
				return observable.Permanent(err)
			}
			return err
		})

	var execErr error
	for item := range tool.ExecuteRx(context.Background(), json.RawMessage(`{}`)).Observe() {
		if item.E != nil {
			execErr = item.E
		}
	}

	fmt.Println(errors.Is(execErr, errBadInput))
	// Output:
	// true
}
