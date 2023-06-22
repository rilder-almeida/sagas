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
				plan: plan{
					notificationA.identifier: {
						notificationA.event: []*Action{actionA},
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
	notificationC, _ := NewNotification(identifier("test2"), Canceled)
	notificationD, _ := NewNotification(identifier("test"), Canceled)
	actionNilFn := func() ActionFn { return func(context.Context) error { return nil } }
	actionErrFn := func() ActionFn { return func(context.Context) error { return errors.New("error") } }
	actionNil := NewAction(actionNilFn())
	actionErr := NewAction(actionErrFn())

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
			name: "[SUCCESS] Run a execution plan",
			args: args{
				notification: notificationB,
			},
			expectedError: "error",
		},

		{
			name: "[SUCCESS] Run a execution plan with an notification that does not exist",
			args: args{
				notification: notificationC,
			},
			expectedError: "",
		},

		{
			name: "[SUCCESS] Run a execution plan with an event that does not exist",
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

				np.Add(notificationA, actionNil)
				np.Add(notificationB, actionErr)

				np.Run(context.Background(), test.args.notification)

				if test.expectedError != "" {
					assert.Equal(t, test.expectedError, err(np).Error())
					return
				}
				assert.NoError(t, err(np))
			})
		})
	}
}

func err(xp *ExecutionPlan) error {
	var errs []error
	for _, events := range xp.plan {
		for _, actions := range events {
			for _, action := range actions {
				if action.getResult() != nil {
					errs = append(errs, action.getResult())
					fmt.Println(action.getResult())
				}
			}
		}
	}

	err := errors.Join(errs...)
	return err
}
