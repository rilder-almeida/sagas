package sagas

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_saga_AddStep(t *testing.T) {
	t.Parallel()

	type args struct {
		starter     Step
		middle      Step
		shouldPanic bool
	}

	tests := []struct {
		name string
		args args
		want Step
	}{
		{
			name: "[SUCCESS] Should add a starter and middle steps to the saga",
			args: args{
				starter:     NewStep("test", func(context.Context) error { return nil }, nil),
				middle:      NewStep("test", func(context.Context) error { return nil }, nil),
				shouldPanic: false,
			},
			want: NewStep("test", func(context.Context) error { return nil }, nil),
		},

		{
			name: "[SUCCESS] Should add a starter step to the saga",
			args: args{
				starter:     NewStep("test", func(context.Context) error { return nil }, nil),
				shouldPanic: false,
			},
			want: NewStep("test", func(context.Context) error { return nil }, nil),
		},

		{
			name: "[PANIC] Should panic when adding a nil starter step to the saga",
			args: args{
				starter:     nil,
				shouldPanic: true,
			},
			want: nil,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.args.shouldPanic {
				assert.Panics(t, func() {
					c := NewSaga()
					c.AddSteps(test.args.starter, test.args.middle)
				})
				return
			}
			assert.NotPanics(t, func() {
				c := NewSaga()
				c.AddSteps(test.args.starter, test.args.middle)
				assert.Equal(t, test.want.Run(context.Background()), c.(*saga).Steps.starter.Run(context.Background()))
				if test.args.middle != nil {
					assert.Equal(t, test.want.Run(context.Background()), c.(*saga).Steps.middles[0].Run(context.Background()))
				}
			})
		})
	}
}

func Test_saga_Run(t *testing.T) {
	t.Parallel()

	total := 0

	type args struct {
		starter Step
		middle  Step
		event   Event
	}

	plusTenStepFn := func(ctx context.Context, value *int) ActionFn {
		return func(ctx context.Context) error {
			*value += 10
			return nil
		}
	}

	minusTenStepFn := func(ctx context.Context, value *int) ActionFn {
		return func(ctx context.Context) error {
			*value -= 10
			return nil
		}
	}

	failedStepFn := func(ctx context.Context, value *int) ActionFn {
		return func(ctx context.Context) error {
			return fmt.Errorf("failed")
		}
	}

	tests := []struct {
		name string
		args args
		want int
	}{
		{
			name: "[SUCCESS] Should run a saga with a starter and middle steps and return the final value = 0",
			args: args{
				starter: NewStep("starter", plusTenStepFn(context.Background(), &total), nil),
				middle:  NewStep("middle", minusTenStepFn(context.Background(), &total), nil),
				event:   Completed,
			},
			want: 0,
		},

		{
			name: "[SUCCESS] Should run a saga with a starter and middle steps and return the final value = 20",
			args: args{
				starter: NewStep("starter", plusTenStepFn(context.Background(), &total), nil),
				middle:  NewStep("middle", plusTenStepFn(context.Background(), &total), nil),
				event:   Completed,
			},
			want: 20,
		},

		{
			name: "[SUCCESS] Should run a saga with a starter and middle steps and return the final value = -20",
			args: args{
				starter: NewStep("starter", minusTenStepFn(context.Background(), &total), nil),
				middle:  NewStep("middle", minusTenStepFn(context.Background(), &total), nil),
				event:   Completed,
			},
			want: -20,
		},

		{
			name: "[SUCCESS] Should run a saga with a starter and middle steps and return the final value = -10",
			args: args{
				starter: NewStep("starter", failedStepFn(context.Background(), &total), nil),
				middle:  NewStep("middle", minusTenStepFn(context.Background(), &total), nil),
				event:   Failed,
			},
			want: -10,
		},

		{
			name: "[SUCCESS] Should run a saga with a starter and middle steps and return the final value = 0",
			args: args{
				starter: NewStep("starter", failedStepFn(context.Background(), &total), nil),
				middle:  NewStep("middle", failedStepFn(context.Background(), &total), nil),
				event:   Failed,
			},
			want: 0,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				total = 0
				c := NewSaga()
				c.AddSteps(test.args.starter, test.args.middle)
				c.When(test.args.starter).Is(test.args.event).Then(NewAction(test.args.middle.Run)).Plan()
				c.Run(context.Background(), func() bool { return test.args.middle.GetState() == Completed })
				assert.Equal(t, test.want, total)
			})
		})
	}
}
