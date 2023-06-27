package sagas

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_action_run(t *testing.T) {
	t.Parallel()

	type args struct {
		fn ActionFn
	}

	tests := []struct {
		name          string
		args          args
		expectedError string
	}{
		{
			name: "[SUCCESS] actionFn returns nil",
			args: args{
				fn: func(ctx context.Context) error {
					return nil
				},
			},
			expectedError: "",
		},

		{
			name: "[SUCCESS] actionFn returns error",
			args: args{
				fn: func(ctx context.Context) error {
					return errors.New("error")
				},
			},
			expectedError: "error",
		},

		{
			name: "[SUCCESSa] actionFn panics",
			args: args{
				fn: func(ctx context.Context) error {
					panic("panic")
				},
			},
			expectedError: "",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {

				a := NewAction(test.args.fn)
				err := a.run(context.Background())

				if test.expectedError == "" {
					assert.NoError(t, err)
				} else {
					assert.Equal(t, test.expectedError, err.Error())
				}
			})
		})
	}
}
