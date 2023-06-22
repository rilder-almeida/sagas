package sagas

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_defaultClassifier_Classify(t *testing.T) {
	t.Parallel()

	type args struct {
		err error
	}

	tests := []struct {
		name string
		args args
		want Status
	}{
		{
			name: "[SUCESS] Should return Successedif the error is nil",
			args: args{
				err: nil,
			},
			want: Successed,
		},

		{
			name: "[SUCESS] Should return Retry if the error is not nil",
			args: args{
				err: assert.AnError,
			},
			want: Retry,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				c := NewDefaultClassifier()
				got := c.Classify(test.args.err)
				assert.Equal(t, test.want, got)
			})
		})
	}
}

func Test_whitelistClassifier_Classify(t *testing.T) {
	t.Parallel()

	type args struct {
		errList []error
		err     error
	}

	tests := []struct {
		name string
		args args
		want Status
	}{
		{
			name: "[SUCESS] Should return Successed if the error is nil",
			args: args{
				errList: []error{
					assert.AnError,
				},
				err: nil,
			},
			want: Successed,
		},

		{
			name: "[SUCESS] Should return Retry if the error is in the whitelist",
			args: args{
				errList: []error{
					assert.AnError,
				},
				err: assert.AnError,
			},
			want: Retry,
		},

		{
			name: "[SUCESS] Should return Failed if the error is not in the whitelist",
			args: args{
				errList: []error{
					assert.AnError,
				},
				err: errors.New("not in the whitelist"),
			},
			want: Failed,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				c := NewWhitelistClassifier(test.args.errList...)
				got := c.Classify(test.args.err)
				assert.Equal(t, test.want, got)
			})
		})
	}
}

func Test_blacklistClassifier_Classify(t *testing.T) {
	t.Parallel()

	type args struct {
		errList []error
		err     error
	}

	tests := []struct {
		name string
		args args
		want Status
	}{
		{
			name: "[SUCESS] Should return Successed if the error is nil",
			args: args{
				errList: []error{
					assert.AnError,
				},
				err: nil,
			},
			want: Successed,
		},

		{
			name: "[SUCESS] Should return Failed if the error is in the blacklist",
			args: args{
				errList: []error{
					assert.AnError,
				},
				err: assert.AnError,
			},
			want: Failed,
		},

		{
			name: "[SUCESS] Should return Retry if the error is not in the blacklist",
			args: args{
				errList: []error{
					assert.AnError,
				},
				err: errors.New("not in the blacklist"),
			},
			want: Retry,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				c := NewBlacklistClassifier(test.args.errList...)
				got := c.Classify(test.args.err)
				assert.Equal(t, test.want, got)
			})
		})
	}
}
