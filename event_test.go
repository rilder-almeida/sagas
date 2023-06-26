package sagas

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_status_string(t *testing.T) {
	t.Parallel()

	type args struct {
		s Status
	}

	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "[SUCCESS] Status Undefined",
			args: args{
				s: Undefined,
			},
			want: "Undefined",
		},

		{
			name: "[SUCCESS] Status Successed",
			args: args{
				s: Successed,
			},
			want: "Successed",
		},

		{
			name: "[SUCCESS] Status Retry",
			args: args{
				s: retry,
			},
			want: "Retry",
		},

		{
			name: "[SUCCESS] Status Failed",
			args: args{
				s: Failed,
			},
			want: "Failed",
		},

		{
			name: "[SUCCESS] Status Failed",
			args: args{
				s: Status(99),
			},
			want: "invalid status",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				got := test.args.s.String()
				assert.Equal(t, test.want, got)
			})
		})
	}
}

func Test_state_string(t *testing.T) {
	t.Parallel()

	type args struct {
		s State
	}

	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "[SUCCESS] State Idle",
			args: args{
				s: Idle,
			},
			want: "Idle",
		},

		{
			name: "[SUCCESS] State Running",
			args: args{
				s: Running,
			},
			want: "Running",
		},

		{
			name: "[SUCCESS] State Completed",
			args: args{
				s: Completed,
			},
			want: "Completed",
		},

		{
			name: "[SUCCESS] State Failed",
			args: args{
				s: State(99),
			},
			want: "invalid state",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				got := test.args.s.String()
				assert.Equal(t, test.want, got)
			})
		})
	}
}
