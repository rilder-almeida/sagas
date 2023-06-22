package sagas

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_executionPlan_Add(t *testing.T) {
	t.Parallel()

	notificationA, _ := NewNotification(identifier("test"), Undefined)
	actionA := func() Action { return func(context.Context) error { return nil } }
	actionsA := Actions{
		actionA(),
	}

	type args struct {
		notification notification
	}

	tests := []struct {
		name string
		args args
		want *ExecutionPlan
	}{
		{
			name: "[SUCCESS] Add a new notification to a new identifier",
			args: args{
				notification: notificationA,
			},
			want: &ExecutionPlan{
				notificationA.identifier: map[event]Actions{
					notificationA.event: actionsA,
				},
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				np := NewExecutionPlan()
				np.Add(test.args.notification, actionsA)
				eq := fmt.Sprint(test.want) == fmt.Sprint(np)
				assert.True(t, eq)
			})
		})
	}
}

func Test_executionPlan_Run(t *testing.T) {
	t.Parallel()

	notificationA, _ := NewNotification(identifier("test"), Undefined)
	notificationB, _ := NewNotification(identifier("test1"), Completed)
	notificationC, _ := NewNotification(identifier("test2"), Canceled)
	notificationD, _ := NewNotification(identifier("test"), Canceled)
	actionNil := func() Action { return func(context.Context) error { return nil } }
	actionErr := func() Action { return func(context.Context) error { return errors.New("error") } }
	actionsA := Actions{
		actionNil(),
		actionNil(),
	}
	actionsB := Actions{
		actionErr(),
		actionErr(),
	}

	type args struct {
		notification notification
	}

	tests := []struct {
		name          string
		args          args
		expectedError string
	}{
		{
			name: "[SUCCESS] Run a execution plan",
			args: args{
				notification: notificationA,
			},
			expectedError: "",
		},

		{
			name: "[ERROR] Run a execution plan",
			args: args{
				notification: notificationB,
			},
			expectedError: "errors while executing actions: error; error",
		},

		{
			name: "[ERROR] Run a execution plan with an notification that does not exist",
			args: args{
				notification: notificationC,
			},
			expectedError: "",
		},

		{
			name: "[ERROR] Run a execution plan with an event that does not exist",
			args: args{
				notification: notificationD,
			},
			expectedError: "",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {

				np := NewExecutionPlan()

				np.Add(notificationA, actionsA)
				np.Add(notificationB, actionsB)

				err := np.Run(context.Background(), test.args.notification)

				if err != nil {
					assert.Equal(t, test.expectedError, err.Error())
					return
				}
				assert.NoError(t, err)
			})
		})
	}
}
