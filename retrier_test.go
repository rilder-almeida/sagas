package sagas

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func Test_retrier_Retry(t *testing.T) {
	t.Parallel()

	type args struct {
		backoff    []time.Duration
		classifier classifier
		ctx        func() (context.Context, context.CancelFunc)
		actionFn   func(context.Context, interface{}) Action
		input      interface{}
	}

	tests := []struct {
		name          string
		args          args
		want          interface{}
		expectedError string
	}{
		{
			name: "[SUCCESS] Should return output and no error - ConstantBackoff, nil classifier",
			args: args{
				backoff:    ConstantBackoff(3, 1*time.Second),
				classifier: nil,
				ctx:        func() (context.Context, context.CancelFunc) { return context.Background(), nil },
				actionFn: func(ctx context.Context, input interface{}) Action {
					return func(ctx context.Context) error {
						return nil
					}
				},
				input: "input",
			},
			want: "input",
		},
		{
			name: "[SUCCESS] Should return output and no error - ConstantBackoff, DefaultClassifier",
			args: args{
				backoff:    ConstantBackoff(3, 1*time.Second),
				classifier: NewDefaultClassifier(),
				ctx:        func() (context.Context, context.CancelFunc) { return context.Background(), nil },
				actionFn: func(ctx context.Context, input interface{}) Action {
					return func(ctx context.Context) error {
						return nil
					}
				},
				input: "input",
			},
			want: "input",
		},
		{
			name: "[ERROR] Should return output and error - ConstantBackoff, DefaultClassifier",
			args: args{
				backoff:    ConstantBackoff(1, 1*time.Second),
				classifier: NewDefaultClassifier(),
				ctx:        func() (context.Context, context.CancelFunc) { return context.Background(), nil },
				actionFn: func(ctx context.Context, input interface{}) Action {
					return func(ctx context.Context) error {
						return errors.New("error")
					}
				},
				input: "input",
			},
			want:          "input",
			expectedError: "error",
		},
		{
			name: "[ERROR] Should return output and timeout error - ConstantBackoff, DefaultClassifier",
			args: args{
				backoff:    ConstantBackoff(1, 5*time.Second),
				classifier: NewDefaultClassifier(),
				ctx: func() (context.Context, context.CancelFunc) {
					return context.WithTimeout(context.Background(), 1*time.Second)
				},
				actionFn: func(ctx context.Context, input interface{}) Action {
					return func(ctx context.Context) error {
						return errors.New("error")
					}
				},
				input: "input",
			},
			want:          "input",
			expectedError: "context deadline exceeded",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				r := NewRetrier(test.args.backoff, test.args.classifier)
				ctx, _ := test.args.ctx()
				err := r.Retry(ctx, test.args.actionFn(ctx, test.args.input))
				if test.expectedError == "" {
					assert.NoError(t, err)
				} else {
					assert.Equal(t, test.expectedError, err.Error())
				}
			})
		})
	}
}
