package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"api-gateway/model"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const stepTimeout = 30 * time.Second

// ExecutionEvent is published to Redis pub/sub after each step and on finish.
type ExecutionEvent struct {
	Type        string `json:"type"` // "step_done" | "finished"
	ExecutionID uint   `json:"executionId"`
	StepID      string `json:"stepId,omitempty"`
	Status      string `json:"status"`
	DurationMs  int64  `json:"durationMs,omitempty"`
}

// RunWorkflow executes all steps sequentially and publishes progress events.
// Each step's output becomes the next step's input.
func RunWorkflow(ctx context.Context, db *gorm.DB, rdb *goredis.Client, execution *model.Execution, wf *model.Workflow) {
	channel := fmt.Sprintf("executions:%d", wf.ID)

	var steps []StepDef
	if err := json.Unmarshal(wf.Steps, &steps); err != nil || len(steps) == 0 {
		finishExecution(db, rdb, channel, execution, "success")
		return
	}

	input := map[string]any{}

	for _, stepDef := range steps {
		stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
		startedAt := time.Now()

		runner, factoryErr := NewStepRunner(stepDef)

		var (
			output     map[string]any
			status     = "success"
			stepErrMsg string
		)

		if factoryErr != nil {
			status = "failed"
			stepErrMsg = factoryErr.Error()
		} else {
			var runErr error
			output, runErr = runner.Run(stepCtx, input)
			if runErr != nil {
				status = "failed"
				stepErrMsg = runErr.Error()
			}
		}
		cancel()

		durationMs := time.Since(startedAt).Milliseconds()
		inputJSON, _ := json.Marshal(input)
		outputJSON, _ := json.Marshal(output)

		db.Create(&model.StepLog{
			ExecutionID: execution.ID,
			StepID:      stepDef.ID,
			StepType:    stepDef.Type,
			Status:      status,
			Input:       datatypes.JSON(inputJSON),
			Output:      datatypes.JSON(outputJSON),
			Error:       stepErrMsg,
			DurationMs:  durationMs,
		})

		publishEvent(rdb, channel, ExecutionEvent{
			Type:        "step_done",
			ExecutionID: execution.ID,
			StepID:      stepDef.ID,
			Status:      status,
			DurationMs:  durationMs,
		})

		if status == "failed" {
			finishExecution(db, rdb, channel, execution, "failed")
			return
		}

		input = output
	}

	finishExecution(db, rdb, channel, execution, "success")
}

func finishExecution(db *gorm.DB, rdb *goredis.Client, channel string, execution *model.Execution, status string) {
	now := time.Now()
	db.Model(execution).Updates(map[string]any{
		"status":      status,
		"finished_at": &now,
	})
	publishEvent(rdb, channel, ExecutionEvent{
		Type:        "finished",
		ExecutionID: execution.ID,
		Status:      status,
	})
}

func publishEvent(rdb *goredis.Client, channel string, event ExecutionEvent) {
	data, _ := json.Marshal(event)
	rdb.Publish(context.Background(), channel, string(data))
}
