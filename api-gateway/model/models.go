package model

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type Workflow struct {
	gorm.Model
	Name          string         `gorm:"not null"`
	TriggerType   string         `gorm:"not null"` // webhook, schedule, manual
	TriggerConfig datatypes.JSON
	Steps         datatypes.JSON
	IsActive      bool `gorm:"default:true"`
}

type Execution struct {
	gorm.Model
	WorkflowID     uint
	Workflow       Workflow
	Status         string `gorm:"default:running"` // running, success, failed
	StartedAt      time.Time
	FinishedAt     *time.Time
	TriggerPayload datatypes.JSON
	StepLogs       []StepLog
}

type StepLog struct {
	gorm.Model
	ExecutionID uint
	StepID      string
	StepType    string
	Status      string // success, failed, skipped
	Input       datatypes.JSON
	Output      datatypes.JSON
	Error       string
	DurationMs  int64
}
