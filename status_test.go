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
			name: "[SUCCESS] Status Canceled",
			args: args{
				s: Canceled,
			},
			want: "Canceled",
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
				s: Retry,
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
				got := test.args.s.string()
				assert.Equal(t, test.want, got)
			})
		})
	}
}
