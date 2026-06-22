// Bài tập Go cơ bản: goroutine, channel, context, interface, error handling
// Chạy: go run playground/step_runner_demo.go
package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"
)

// --- Custom error type ---

type StepError struct {
	StepID string
	Reason string
}

func (e *StepError) Error() string {
	return fmt.Sprintf("step %s failed: %s", e.StepID, e.Reason)
}

// --- StepRunner interface ---

type StepResult struct {
	StepID string
	Output map[string]any
	Err    error
}

type StepRunner interface {
	Run(ctx context.Context, input map[string]any) (map[string]any, error)
}

// HTTPStepRunner giả lập gọi HTTP
type HTTPStepRunner struct {
	URL string
}

func (r *HTTPStepRunner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
	// Giả lập network delay
	delay := time.Duration(rand.Intn(200)+100) * time.Millisecond
	select {
	case <-time.After(delay):
		return map[string]any{"status": 200, "url": r.URL}, nil
	case <-ctx.Done():
		return nil, &StepError{StepID: "http", Reason: "context cancelled: " + ctx.Err().Error()}
	}
}

// TransformStepRunner giả lập biến đổi data
type TransformStepRunner struct {
	OutputKey string
}

func (r *TransformStepRunner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
	select {
	case <-ctx.Done():
		return nil, &StepError{StepID: "transform", Reason: ctx.Err().Error()}
	default:
		return map[string]any{r.OutputKey: fmt.Sprintf("transformed(%v)", input)}, nil
	}
}

// --- Demo 1: Fan-out — chạy nhiều step song song ---

func runFanOut() {
	fmt.Println("\n=== Demo 1: Fan-out với goroutine ===")

	runners := []struct {
		id     string
		runner StepRunner
	}{
		{"step1", &HTTPStepRunner{URL: "https://api.example.com/users"}},
		{"step2", &HTTPStepRunner{URL: "https://api.example.com/products"}},
		{"step3", &TransformStepRunner{OutputKey: "summary"}},
		{"step4", &HTTPStepRunner{URL: "https://api.example.com/orders"}},
		{"step5", &TransformStepRunner{OutputKey: "report"}},
	}

	results := make(chan StepResult, len(runners))
	ctx := context.Background()

	for _, s := range runners {
		s := s // capture loop var
		go func() {
			out, err := s.runner.Run(ctx, map[string]any{"from": s.id})
			results <- StepResult{StepID: s.id, Output: out, Err: err}
		}()
	}

	for range runners {
		r := <-results
		if r.Err != nil {
			fmt.Printf("  [%s] ERROR: %v\n", r.StepID, r.Err)
		} else {
			fmt.Printf("  [%s] OK: %v\n", r.StepID, r.Output)
		}
	}
}

// --- Demo 2: Context timeout ---

func runWithTimeout() {
	fmt.Println("\n=== Demo 2: Context timeout ===")

	// HTTPStepRunner với delay ngẫu nhiên, timeout 150ms
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	runner := &HTTPStepRunner{URL: "https://slow-api.example.com"}
	out, err := runner.Run(ctx, nil)
	if err != nil {
		var stepErr *StepError
		if errors.As(err, &stepErr) {
			fmt.Printf("  StepError caught — StepID: %s, Reason: %s\n", stepErr.StepID, stepErr.Reason)
		} else {
			fmt.Printf("  Unknown error: %v\n", err)
		}
	} else {
		fmt.Printf("  Success: %v\n", out)
	}
}

// --- Demo 3: Sequential workflow execution với done channel ---

func runSequential() {
	fmt.Println("\n=== Demo 3: Sequential steps với done channel ===")

	steps := []struct {
		id     string
		runner StepRunner
	}{
		{"fetch-users", &HTTPStepRunner{URL: "https://api.example.com/users"}},
		{"transform", &TransformStepRunner{OutputKey: "processed"}},
		{"notify", &HTTPStepRunner{URL: "https://api.example.com/notify"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		input := map[string]any{}
		for _, s := range steps {
			out, err := s.runner.Run(ctx, input)
			if err != nil {
				fmt.Printf("  [%s] FAILED: %v — stopping workflow\n", s.id, err)
				return
			}
			fmt.Printf("  [%s] OK → output: %v\n", s.id, out)
			input = out // output của step này là input của step tiếp theo
		}
		fmt.Println("  Workflow completed successfully")
	}()

	<-done
}

func main() {
	rand.New(rand.NewSource(time.Now().UnixNano()))

	runFanOut()
	runWithTimeout()
	runSequential()
}
