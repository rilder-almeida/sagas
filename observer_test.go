package sagas

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_NewObserver(t *testing.T) {
	t.Parallel()

	type args struct {
		executionPlan *ExecutionPlan
	}

	tests := []struct {
		name string
		args args
		want *observer
	}{
		{
			name: "[SUCCESS] Should return a new observer",
			args: args{
				executionPlan: NewExecutionPlan(),
			},
			want: &observer{
				ExecutionPlan: NewExecutionPlan(),
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				got := MustNewObserver(test.args.executionPlan)
				assert.Equal(t, test.want.ExecutionPlan, got.ExecutionPlan)
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
		notification  notification
		executionPlan *ExecutionPlan
	}

	tests := []struct {
		name        string
		args        args
		want        notification
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
					o := MustNewObserver(test.args.executionPlan)
					o.Execute(context.Background(), test.args.notification)
				})
				return
			}

			assert.NotPanics(t, func() {
				o := MustNewObserver(test.args.executionPlan)
				o.Execute(context.Background(), test.args.notification)
			})
		})
	}
}
