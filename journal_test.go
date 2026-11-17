package sagas

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_EventKind_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind EventKind
		want string
	}{
		{name: "[Success] When: the zero kind ; Should: return invalid", kind: EventInvalid, want: "invalid"},
		{
			name: "[Success] When: EventSagaStarted ; Should: return saga started",
			kind: EventSagaStarted, want: "saga started",
		},
		{
			name: "[Success] When: EventStepStarted ; Should: return step started",
			kind: EventStepStarted, want: "step started",
		},
		{
			name: "[Success] When: EventStepSucceeded ; Should: return step succeeded",
			kind: EventStepSucceeded, want: "step succeeded",
		},
		{
			name: "[Success] When: EventStepFailed ; Should: return step failed",
			kind: EventStepFailed, want: "step failed",
		},
		{
			name: "[Success] When: EventStepUnknown ; Should: return step unknown",
			kind: EventStepUnknown, want: "step unknown",
		},
		{
			name: "[Success] When: EventStepSkipped ; Should: return step skipped",
			kind: EventStepSkipped, want: "step skipped",
		},
		{
			name: "[Success] When: EventCompensationStarted ; Should: return compensation started",
			kind: EventCompensationStarted, want: "compensation started",
		},
		{
			name: "[Success] When: EventCompensationSucceeded ; Should: return compensation succeeded",
			kind: EventCompensationSucceeded, want: "compensation succeeded",
		},
		{
			name: "[Success] When: EventCompensationFailed ; Should: return compensation failed",
			kind: EventCompensationFailed, want: "compensation failed",
		},
		{
			name: "[Success] When: EventCompensationBlocked ; Should: return compensation blocked",
			kind: EventCompensationBlocked, want: "compensation blocked",
		},
		{
			name: "[Success] When: EventCompensationHalted ; Should: return compensation halted",
			kind: EventCompensationHalted, want: "compensation halted",
		},
		{
			name: "[Success] When: EventSagaFinished ; Should: return saga finished",
			kind: EventSagaFinished, want: "saga finished",
		},
		{
			name: "[Fail] When: the kind is out of range ; Should: return EventKind(99)",
			kind: EventKind(99), want: "EventKind(99)",
		},
		{
			name: "[Fail] When: the kind is negative ; Should: return EventKind(-1)",
			kind: EventKind(-1), want: "EventKind(-1)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.kind.String())
		})
	}
}

func Test_Zone_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		zone Zone
		want string
	}{
		{name: "[Success] When: the zero zone ; Should: return invalid", zone: ZoneInvalid, want: "invalid"},
		{
			name: "[Success] When: ZoneCompensable ; Should: return compensable",
			zone: ZoneCompensable, want: "compensable",
		},
		{name: "[Success] When: ZonePivot ; Should: return pivot", zone: ZonePivot, want: "pivot"},
		{
			name: "[Success] When: ZoneForwardOnly ; Should: return forward-only",
			zone: ZoneForwardOnly, want: "forward-only",
		},
		{name: "[Fail] When: the zone is out of range ; Should: return Zone(99)", zone: Zone(99), want: "Zone(99)"},
		{name: "[Fail] When: the zone is negative ; Should: return Zone(-1)", zone: Zone(-1), want: "Zone(-1)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.zone.String())
		})
	}
}
