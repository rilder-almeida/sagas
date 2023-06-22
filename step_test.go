package sagas

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func makeActionNoError(ctx context.Context) ActionFn {
	return func(ctx context.Context) error {
		return nil
	}
}

func makeActionError(ctx context.Context) ActionFn {
	return func(ctx context.Context) error {
		return errors.New("action failed")
	}
}

func Test_MustNew(t *testing.T) {
	t.Parallel()

	type args struct {
		name          string
		action        ActionFn
		executionPlan *ExecutionPlan
	}

	tests := []struct {
		name        string
		args        args
		wantName    string
		wantAction  ActionFn
		shouldPanic bool
	}{
		{
			name: "[SUCCESS] Should return a compensation",
			args: args{
				name:          "test",
				action:        makeActionNoError(context.Background()),
				executionPlan: NewExecutionPlan(),
			},
			wantName:    "test",
			wantAction:  makeActionNoError(context.Background()),
			shouldPanic: false,
		},

		{
			name: "[PANIC]] Should panic if action is nil",
			args: args{
				name:          "test",
				action:        nil,
				executionPlan: NewExecutionPlan(),
			},
			wantName:    "",
			wantAction:  nil,
			shouldPanic: true,
		},

		{
			name: "[PANIC]] Should panic if name is empty",
			args: args{
				name:          "",
				action:        makeActionNoError(context.Background()),
				executionPlan: NewExecutionPlan(),
			},
			wantName:    "",
			wantAction:  nil,
			shouldPanic: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.shouldPanic {
				assert.Panics(t, func() {
					NewStep(test.args.name, test.args.action, nil)
				})
				return
			}

			assert.NotPanics(t, func() {
				got := NewStep(test.args.name, test.args.action, nil)
				err := got.Run(context.Background())
				assert.NoError(t, err)
				assert.Equal(t, Successed, got.GetStatus())
				assert.Equal(t, Completed, got.GetState())
				assert.IsType(t, NewIdentifier("string"), got.getIdentifier())
				assert.Equal(t, test.wantName, got.GetName())
			})
		})
	}
}

func Test_step_Run(t *testing.T) {
	t.Parallel()

	type args struct {
		input  interface{}
		action ActionFn
	}

	tests := []struct {
		name          string
		args          args
		want          interface{}
		expectedError string
	}{
		{
			name: "[SUCCESS] Should return the output of the action",
			args: args{
				input:  "input",
				action: makeActionNoError(context.Background()),
			},
			want: "input",
		},

		{
			name: "[ERROR] Should return the error of the action",
			args: args{
				input:  "input",
				action: makeActionError(context.Background()),
			},
			want:          nil,
			expectedError: "action failed",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				s := NewStep("test", test.args.action, nil)
				err := s.Run(context.Background())

				if test.expectedError == "" {
					assert.NoError(t, err)
					assert.Equal(t, Successed, s.GetStatus())
				} else {
					assert.Equal(t, test.expectedError, err.Error())
					assert.Equal(t, Failed, s.GetStatus())
				}
			})
		})
	}
}

func Test_step_Run_WithRetry(t *testing.T) {
	t.Parallel()

	type args struct {
		input  interface{}
		action ActionFn
	}

	tests := []struct {
		name          string
		args          args
		want          interface{}
		expectedError string
	}{
		{
			name: "[SUCCESS] Should return the output of the action with retry but no error",
			args: args{
				input:  "input",
				action: makeActionNoError(context.Background()),
			},
			want: "input",
		},

		{
			name: "[ERROR] Should return the error of the action with retry",
			args: args{
				input:  "input",
				action: makeActionError(context.Background()),
			},
			want:          nil,
			expectedError: "action failed",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				s := NewStep("test", test.args.action, NewRetrier(ConstantBackoff(1, 1), nil))
				err := s.Run(context.Background())

				if test.expectedError == "" {
					assert.NoError(t, err)
				} else {
					assert.Equal(t, test.expectedError, err.Error())
				}
			})
		})
	}
}

func Test_step_GetObserver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want *observer
	}{
		{
			name: "Should return the observer",
			want: MustNewObserver(NewExecutionPlan()),
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				step := NewStep("test", makeActionNoError(context.Background()), nil)
				step.setObserver(MustNewObserver(NewExecutionPlan()))
				got := step.getObserver()
				assert.Equal(t, test.want, got)
			})
		})
	}
}

func Test_step_GetONotifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want *notifier
	}{
		{
			name: "Should return the notifier",
			want: NewNotifier(),
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				step := NewStep("test", makeActionNoError(context.Background()), nil)
				got := step.getNotifier()
				assert.Equal(t, test.want, got)
			})
		})
	}
}
