package sagas

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_NewNotifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want *notifier
	}{
		{
			name: "[SUCCESS] Should return a new Notifier",
			want: &notifier{
				observers: make([]Observer, 0),
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				got := NewNotifier()
				assert.Equal(t, test.want, got)
			})
		})
	}
}

func Test_notifier_Add(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want *notifier
	}{
		{
			name: "[SUCCESS] Should add an observer to the Notifier",
			want: &notifier{
				observers: []Observer{
					&observer{
						executionPlan: NewExecutionPlan(),
					},
				},
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				n := NewNotifier()
				n.Add(NewObserver(NewExecutionPlan()))
				assert.Equal(t, test.want, n)
			})
		})
	}
}

func Test_notifier_Notify(t *testing.T) {
	t.Parallel()

	n, _ := NewNotification(NewIdentifier("test"), Running)

	type args struct {
		notification Notification
		observer     Observer
	}

	tests := []struct {
		name string
		args args
		want Notification
	}{
		{
			name: "[SUCCESS] Should notify all observers",
			args: args{
				notification: n,
				observer: &observer{
					executionPlan: NewExecutionPlan(),
				},
			},
			want: n,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				n := NewNotifier()
				n.Add(test.args.observer)
				n.Notify(context.Background(), test.args.notification)
				assert.Equal(t, test.want, test.args.notification)
			})
		})
	}
}
