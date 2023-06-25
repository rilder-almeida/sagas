package sagas

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_executionPlan_Add(t *testing.T) {
	t.Parallel()

	notificationA, _ := NewNotification(identifier("test"), Undefined)
	actionAFn := func() ActionFn { return func(context.Context) error { return nil } }
	actionA := NewAction(actionAFn())

	type args struct {
		notification Notification
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
				Plan: plan{
					notificationA.Identifier: {
						notificationA.Event: []Action{actionA},
					},
				},
				mutex: sync.Mutex{},
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				np := NewExecutionPlan()
				np.Add(test.args.notification, actionA)
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
	notificationC, _ := NewNotification(identifier("test2"), Failed)
	notificationD, _ := NewNotification(identifier("test"), Failed)
	actionNilFn := func() ActionFn { return func(context.Context) error { return nil } }
	actionErrFn := func() ActionFn { return func(context.Context) error { return errors.New("error") } }
	actionNil := NewAction(actionNilFn())
	actionErr := NewAction(actionErrFn())

	type args struct {
		notificationAdded  Notification
		Action             Action
		notificationSended Notification
	}

	tests := []struct {
		name          string
		args          args
		expectedError string
	}{
		{
			name: "[SUCCESS] Run a execution plan",
			args: args{
				notificationAdded:  notificationA,
				Action:             actionNil,
				notificationSended: notificationA,
			},
			expectedError: "",
		},

		{
			name: "[SUCCESS] Run a execution plan with an notification that does not exist.",
			args: args{
				notificationAdded:  notificationA,
				Action:             actionNil,
				notificationSended: notificationC,
			},
			expectedError: "",
		},

		{
			name: "[SUCCESS] Run a execution plan with an event that does not exist",
			args: args{
				notificationAdded:  notificationA,
				Action:             actionNil,
				notificationSended: notificationD,
			},
			expectedError: "",
		},

		{
			name: "[ERROR] Run a execution plan",
			args: args{
				notificationAdded:  notificationB,
				Action:             actionErr,
				notificationSended: notificationB,
			},
			expectedError: "error",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				np := NewExecutionPlan()
				np.Add(test.args.notificationAdded, test.args.Action)
				np.Run(context.Background(), test.args.notificationSended)
				if test.expectedError != "" {
					assert.Equal(t, test.expectedError, err(np, test.args.notificationSended.Identifier, test.args.notificationSended.Event).Error())
					return
				}
				assert.NoError(t, err(np, test.args.notificationSended.Identifier, test.args.notificationSended.Event))
			})
		})
	}
}

func err(xp *ExecutionPlan, id Identifier, event Event) error {
	var errs []error
	actions, _ := xp.Plan.get(id, event)
	for _, Action := range actions {
		err := Action.run(context.Background())
		if err != nil {
			errs = append(errs, err)
		}
	}

	err := errors.Join(errs...)
	return err
}
