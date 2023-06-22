package sagas

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_String(t *testing.T) {
	t.Parallel()

	type args struct {
		name string
	}

	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "[SUCCESS] Should return a string with the name",
			args: args{
				name: "test",
			},
			want: "test",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				id := NewIdentifier(test.args.name)
				got := id.String()
				assert.NotEmpty(t, got)
				assert.IsType(t, test.want, got)
			})
		})
	}
}

func Test_makeUniqueIdentifier(t *testing.T) {
	t.Parallel()
	t.Run("[SUCCESS] Should return valid uuid", func(t *testing.T) {
		assert.NotPanics(t, func() {
			got := makeUniqueIdentifier("strings")
			assert.NotNil(t, got)
			assert.NotEmpty(t, got)
			assert.IsType(t, "strings", got)
		})
	})

}
