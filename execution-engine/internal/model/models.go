package model

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type Workflow struct {
	gorm.Model
	Name          string
	UserID        uint
	TriggerType   string
	TriggerConfig datatypes.JSON
	Steps         datatypes.JSON
	IsActive      bool
}

type Execution struct {
	gorm.Model
	WorkflowID     uint
	Status         string
	StartedAt      time.Time
	FinishedAt     *time.Time
	TriggerPayload datatypes.JSON
}

type StepLog struct {
	gorm.Model
	ExecutionID uint
	StepID      string
	StepType    string
	Status      string
	Input       datatypes.JSON
	Output      datatypes.JSON
	Error       string
	DurationMs  int64
}
