package sagas

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_compatibleVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{name: "[Success] When: the version equals EventVersion ; Should: accept it", version: "v0.2.0", want: true},
		{name: "[Success] When: only the patch differs ; Should: accept it", version: "v0.2.7", want: true},
		{name: "[Fail] When: an older minor before v1 ; Should: refuse it", version: "v0.1.0", want: false},
		{name: "[Fail] When: a newer minor before v1 ; Should: refuse it", version: "v0.3.0", want: false},
		{name: "[Fail] When: another major ; Should: refuse it", version: "v1.0.0", want: false},
		{name: "[Fail] When: the version is empty ; Should: refuse it", version: "", want: false},
		{name: "[Fail] When: the patch is missing ; Should: refuse it", version: "v0.2", want: false},
		{name: "[Fail] When: the v prefix is missing ; Should: refuse it", version: "0.2.0", want: false},
		{name: "[Fail] When: a pre-release suffix follows ; Should: refuse it", version: "v0.2.0-rc.1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, compatibleVersion(tt.version))
		})
	}
}
