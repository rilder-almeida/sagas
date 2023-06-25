package sagas

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func Test_ConstantBackoff(t *testing.T) {
	t.Parallel()

	type args struct {
		duration time.Duration
		n        int
	}

	tests := []struct {
		name string
		args args
		want []time.Duration
	}{
		{
			name: "[SUCCESS] Should return a slice of time.Duration with 3 elements",
			args: args{
				duration: 1 * time.Second,
				n:        3,
			},
			want: []time.Duration{1 * time.Second, 1 * time.Second, 1 * time.Second},
		},

		{
			name: "[SUCCESS] Should return a slice of time.Duration with 0 elements",
			args: args{
				duration: 1 * time.Second,
				n:        0,
			},
			want: []time.Duration{},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				got := BackoffConstant(test.args.n, test.args.duration)
				assert.Equal(t, test.want, got)
			})
		})
	}
}

func Test_ExponentialBackoff(t *testing.T) {
	t.Parallel()

	type args struct {
		duration time.Duration
		n        int
		r        float64
	}

	tests := []struct {
		name string
		args args
		want []time.Duration
	}{
		{
			name: "[SUCCESS] Should return a slice of time.Duration with 3 elements and rate 2",
			args: args{
				duration: 1 * time.Second,
				n:        3,
				r:        2,
			},
			want: []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second},
		},

		{
			name: "[SUCCESS] Should return a slice of time.Duration with 5 elements and rate 3",
			args: args{
				duration: 1 * time.Second,
				n:        5,
				r:        3,
			},
			want: []time.Duration{1 * time.Second, 3 * time.Second, 9 * time.Second, 27 * time.Second, 81 * time.Second},
		},

		{
			name: "[SUCCESS] Should return a slice of time.Duration with 0 elements",
			args: args{
				duration: 1 * time.Second,
				n:        0,
				r:        2,
			},
			want: []time.Duration{},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				got := BackoffExponential(test.args.n, test.args.duration, test.args.r)
				assert.Equal(t, test.want, got)
			})
		})
	}
}

func Test_LimitedExponentialBackoff(t *testing.T) {
	t.Parallel()

	type args struct {
		duration time.Duration
		limit    time.Duration
		n        int
		r        float64
	}

	tests := []struct {
		name string
		args args
		want []time.Duration
	}{
		{
			name: "[SUCCESS] Should return a slice of time.Duration with 3 elements and rate 2",
			args: args{
				duration: 1 * time.Second,
				limit:    5 * time.Second,
				n:        5,
				r:        2,
			},
			want: []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second},
		},

		{
			name: "[SUCCESS] Should return a slice of time.Duration with 5 elements and rate 3",
			args: args{
				duration: 1 * time.Second,
				limit:    5 * time.Second,
				n:        5,
				r:        3,
			},
			want: []time.Duration{1 * time.Second, 3 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second},
		},

		{
			name: "[SUCCESS] Should return a slice of time.Duration with 0 elements",
			args: args{
				duration: 1 * time.Second,
				limit:    5 * time.Second,
				n:        0,
				r:        2,
			},
			want: []time.Duration{},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				got := BackoffLimitedExponential(test.args.n, test.args.duration, test.args.limit, test.args.r)
				assert.Equal(t, test.want, got)
			})
		})
	}
}
