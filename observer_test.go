package sagas

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_NewObserver(t *testing.T) {
	t.Parallel()

	type args struct {
		expl *ExecutionPlan
	}

	tests := []struct {
		name string
		args args
		want *observer
	}{
		{
			name: "[SUCCESS] Should return a new observer",
			args: args{
				expl: NewExecutionPlan(),
			},
			want: &observer{
				executionPlan: NewExecutionPlan(),
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				got := NewObserver(test.args.expl)
				assert.Equal(t, test.want.executionPlan, got.executionPlan)
			})
		})
	}
}

func Test_observer_GetNotification(t *testing.T) {
	t.Parallel()

	n, _ := NewNotification(
		identifier("test"),
		Idle,
	)

	type args struct {
		notification  Notification
		executionPlan *ExecutionPlan
	}

	tests := []struct {
		name        string
		args        args
		want        Notification
		shouldPanic bool
	}{
		{
			name: "[SUCCESS] Should get a notification",
			args: args{
				notification:  n,
				executionPlan: NewExecutionPlan(),
			},
			shouldPanic: false,
			want:        n,
		},
		{
			name: "[PANIC] Should panic when notification is nil",
			args: args{
				notification:  n,
				executionPlan: nil,
			},
			shouldPanic: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.shouldPanic {
				assert.Panics(t, func() {
					o := NewObserver(test.args.executionPlan)
					o.Execute(context.Background(), test.args.notification)
				})
				return
			}

			assert.NotPanics(t, func() {
				o := NewObserver(test.args.executionPlan)
				o.Execute(context.Background(), test.args.notification)
			})
		})
	}
}
