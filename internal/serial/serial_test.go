package serial

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

type otherKey struct{}

func Test_With(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		parent    func(context.Context) context.Context
		wantOther any
	}{
		{
			name:   "[Success] When: the parent is bare ; Should: return a marked child",
			parent: func(ctx context.Context) context.Context { return ctx },
		},
		{
			name: "[Success] When: the parent holds another value ; Should: keep that value and add the mark",
			parent: func(ctx context.Context) context.Context {
				return context.WithValue(ctx, otherKey{}, "kept")
			},
			wantOther: "kept",
		},
		{
			name:   "[Success] When: the parent is already marked ; Should: stay marked",
			parent: With,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := With(tt.parent(t.Context()))
			assert.True(t, On(got))
			assert.Equal(t, tt.wantOther, got.Value(otherKey{}))
		})
	}
}

func Test_On(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ctx  func(context.Context) context.Context
		want bool
	}{
		{
			name: "[Fail] When: the context is bare ; Should: report no mark",
			ctx:  func(ctx context.Context) context.Context { return ctx },
		},
		{
			name: "[Fail] When: the context holds only another value ; Should: report no mark",
			ctx: func(ctx context.Context) context.Context {
				return context.WithValue(ctx, otherKey{}, true)
			},
		},
		{
			name: "[Success] When: the context is marked ; Should: report the mark",
			ctx:  With,
			want: true,
		},
		{
			name: "[Success] When: the context is a cancelable child of a marked one ; Should: report the mark",
			ctx: func(ctx context.Context) context.Context {
				child, cancel := context.WithCancel(With(ctx))
				t.Cleanup(cancel)

				return child
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, On(tt.ctx(t.Context())))
		})
	}
}
