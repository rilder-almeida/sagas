package sagas

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
