package sagas

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_controller_AddStep(t *testing.T) {
	t.Parallel()

	type args struct {
		starter     *Step
		middle      *Step
		shouldPanic bool
	}

	tests := []struct {
		name string
		args args
		want *Step
	}{
		{
			name: "[SUCCESS] Should add a starter and middle steps to the controller",
			args: args{
				starter:     NewStep("test", func(context.Context) error { return nil }, nil),
				middle:      NewStep("test", func(context.Context) error { return nil }, nil),
				shouldPanic: false,
			},
			want: NewStep("test", func(context.Context) error { return nil }, nil),
		},

		{
			name: "[SUCCESS] Should add a starter step to the controller",
			args: args{
				starter:     NewStep("test", func(context.Context) error { return nil }, nil),
				shouldPanic: false,
			},
			want: NewStep("test", func(context.Context) error { return nil }, nil),
		},

		{
			name: "[PANIC] Should panic when adding a nil starter step to the controller",
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
					c := NewController()
					c.AddSteps(test.args.starter, test.args.middle)
				})
				return
			}
			assert.NotPanics(t, func() {
				c := NewController()
				c.AddSteps(test.args.starter, test.args.middle)
				assert.Equal(t, test.want.Run(context.Background()), c.saga.starter.Run(context.Background()))
				if test.args.middle != nil {
					assert.Equal(t, test.want.Run(context.Background()), c.saga.middles[0].Run(context.Background()))
				}
			})
		})
	}
}

func Test_controller_Go(t *testing.T) {
	t.Parallel()

	type args struct {
		starter *Step
		middle  *Step
	}

	tests := []struct {
		name string
		args args
	}{
		{
			name: "[SUCCESS] Should remove a middle step from the controller",
			args: args{
				starter: NewStep("test", func(context.Context) error { return nil }, nil),
				middle:  NewStep("test", func(context.Context) error { return nil }, nil),
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				c := NewController()
				c.AddSteps(test.args.starter, test.args.middle)
				c.When(test.args.starter).Is(Completed).Then(NewAction(func(ctx context.Context) error { return nil })).Plan()
				observer := NewObserver(c.expl)
				c.setObserver(observer)
				c.centralizeNorifiers()
				c.Run(context.Background(), func() bool { return true })
			})
		})
	}
}
