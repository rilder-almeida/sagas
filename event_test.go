package sagas

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_NewEvent(t *testing.T) {
	t.Parallel()

	type args struct {
		id    identifier
		event event
	}

	tests := []struct {
		name          string
		args          args
		want          notification
		expectedError string
	}{
		{
			name: "[SUCCESS] Should return a new notification when all parameters are valid - State",
			args: args{
				id:    "id",
				event: Idle,
			},
			want: notification{
				identifier: "id",
				event:      Idle,
			},
		},

		{
			name: "[SUCCESS] Should return a new event when all parameters are valid - Status",
			args: args{
				id:    "id",
				event: Undefined,
			},
			want: notification{
				identifier: "id",
				event:      Undefined,
			},
		},

		{
			name: "[Error] Should return a new event when notification is invalid - nil",
			args: args{
				id:    "id",
				event: nil,
			},
			want:          notification{},
			expectedError: "invalid event",
		},

		{
			name: "[Error] Should return a new event when notification is invalid - string",
			args: args{
				id:    "id",
				event: "invalid",
			},
			want:          notification{},
			expectedError: "invalid event",
		},

		{
			name: "[Error] Should return a new event when id is invalid - empty",
			args: args{
				id:    "",
				event: Idle,
			},
			want:          notification{},
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
