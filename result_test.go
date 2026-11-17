package sagas

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_Status_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status Status
		want   string
	}{
		{
			name:   "[Success] When: the zero status ; Should: return unfinished",
			status: StatusUnfinished, want: "unfinished",
		},
		{
			name:   "[Success] When: StatusCompleted ; Should: return completed",
			status: StatusCompleted, want: "completed",
		},
		{
			name:   "[Success] When: StatusCompensated ; Should: return compensated",
			status: StatusCompensated, want: "compensated",
		},
		{
			name:   "[Success] When: StatusCompensationFailed ; Should: return compensation failed",
			status: StatusCompensationFailed, want: "compensation failed",
		},
		{
			name:   "[Success] When: StatusIncomplete ; Should: return incomplete",
			status: StatusIncomplete, want: "incomplete",
		},
		{
			name:   "[Fail] When: the status is out of range ; Should: return Status(99)",
			status: Status(99), want: "Status(99)",
		},
		{
			name:   "[Fail] When: the status is negative ; Should: return Status(-1)",
			status: Status(-1), want: "Status(-1)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.status.String())
		})
	}
}

func Test_StepStatus_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status StepStatus
		want   string
	}{
		{
			name:   "[Success] When: the zero step status ; Should: return not started",
			status: StepNotStarted, want: "not started",
		},
		{
			name:   "[Success] When: StepSucceeded ; Should: return succeeded",
			status: StepSucceeded, want: "succeeded",
		},
		{name: "[Success] When: StepFailed ; Should: return failed", status: StepFailed, want: "failed"},
		{name: "[Success] When: StepUnknown ; Should: return unknown", status: StepUnknown, want: "unknown"},
		{name: "[Success] When: StepSkipped ; Should: return skipped", status: StepSkipped, want: "skipped"},
		{
			name:   "[Success] When: StepCompensated ; Should: return compensated",
			status: StepCompensated, want: "compensated",
		},
		{
			name:   "[Success] When: StepCompensationFailed ; Should: return compensation failed",
			status: StepCompensationFailed, want: "compensation failed",
		},
		{name: "[Success] When: StepBlocked ; Should: return blocked", status: StepBlocked, want: "blocked"},
		{name: "[Success] When: StepHalted ; Should: return halted", status: StepHalted, want: "halted"},
		{
			name:   "[Fail] When: the step status is out of range ; Should: return StepStatus(99)",
			status: StepStatus(99), want: "StepStatus(99)",
		},
		{
			name:   "[Fail] When: the step status is negative ; Should: return StepStatus(-1)",
			status: StepStatus(-1), want: "StepStatus(-1)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.status.String())
		})
	}
}
