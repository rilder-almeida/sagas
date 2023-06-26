package sagas

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_NewEvent(t *testing.T) {
	t.Parallel()

	type args struct {
		id    Identifier
		event Event
	}

	tests := []struct {
		name          string
		args          args
		want          Notification
		expectedError string
	}{
		{
			name: "[SUCCESS] Should return a new notification when all parameters are valid - State",
			args: args{
				id:    identifier("id"),
				event: Idle,
			},
			want: Notification{
				Identifier: identifier("id"),
				Event:      Idle,
			},
		},

		{
			name: "[SUCCESS] Should return a new event when all parameters are valid - Status",
			args: args{
				id:    identifier("id"),
				event: Undefined,
			},
			want: Notification{
				Identifier: identifier("id"),
				Event:      Undefined,
			},
		},

		{
			name: "[Error] Should return a new event when notification is invalid - nil",
			args: args{
				id:    identifier("id"),
				event: nil,
			},
			want:          Notification{},
			expectedError: "invalid event",
		},

		{
			name: "[Error] Should return a new event when notification is invalid - string",
			args: args{
				id:    identifier("id"),
				event: mockEvent{},
			},
			want:          Notification{},
			expectedError: "invalid event",
		},

		{
			name: "[Error] Should return a new event when id is invalid - empty",
			args: args{
				id:    identifier(""),
				event: Idle,
			},
			want:          Notification{},
			expectedError: "invalid identifier",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {

				got, err := NewNotification(test.args.id, test.args.event)

				if test.expectedError == "" {
					assert.NoError(t, err)
					assert.Equal(t, test.want, got)
				} else {
					assert.Equal(t, test.expectedError, err.Error())
				}
			})
		})
	}
}

type mockEvent struct{}

func (m mockEvent) String() string {
	return "mock"
}
