package sagas

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_PanicError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "[Success] When: the value is a string ; Should: print the string",
			value: "bad",
			want:  "sagas: panic: bad",
		},
		{
			name:  "[Success] When: the value is an error ; Should: print the error message",
			value: errBoom,
			want:  "sagas: panic: boom",
		},
		{
			name:  "[Success] When: the value is nil ; Should: print nil",
			value: nil,
			want:  "sagas: panic: <nil>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, (&PanicError{Value: tt.value, Stack: []byte("stack")}).Error())
		})
	}
}
