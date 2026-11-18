package sagas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Saga_Mermaid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		steps []Option
		want  string
	}{
		{
			name: "[Success] When: the saga is the book-trip example ; Should: draw nodes, edges and the class",
			steps: []Option{
				Step("hotel", okAction),
				Step("flight", okAction),
				Step("car", okAction, If(okPredicate)),
				Step("pay", okAction, Pivot(), After("hotel", "flight", "car")),
				Step("receipt", okAction, After("pay")),
			},
			want: "flowchart TD\n" +
				"    s0[\"hotel\"]\n" +
				"    s1[\"flight\"]\n" +
				"    s2[\"car (if)\"]\n" +
				"    s3{{\"pay\"}}\n" +
				"    s4[\"receipt\"]\n" +
				"    s0 --> s3\n" +
				"    s1 --> s3\n" +
				"    s2 --> s3\n" +
				"    s3 --> s4\n" +
				"    classDef forwardOnly stroke-dasharray: 5 5\n" +
				"    class s4 forwardOnly\n",
		},
		{
			name:  "[Success] When: the saga has one step ; Should: draw one node and no edge",
			steps: []Option{Step("hotel", okAction)},
			want:  "flowchart TD\n    s0[\"hotel\"]\n",
		},
		{
			name: "[Success] When: the saga has no pivot ; Should: draw the edges and no class lines",
			steps: []Option{
				Step("hotel", okAction),
				Step("flight", okAction, After("hotel")),
			},
			want: "flowchart TD\n    s0[\"hotel\"]\n    s1[\"flight\"]\n    s0 --> s1\n",
		},
		{
			name: "[Success] When: two steps are forward-only ; Should: list both ids in one class line",
			steps: []Option{
				Step("hotel", okAction),
				Step("pay", okAction, Pivot(), After("hotel")),
				Step("receipt", okAction, After("pay")),
				Step("mail", okAction, After("pay")),
			},
			want: "flowchart TD\n" +
				"    s0[\"hotel\"]\n" +
				"    s1{{\"pay\"}}\n" +
				"    s2[\"receipt\"]\n" +
				"    s3[\"mail\"]\n" +
				"    s0 --> s1\n" +
				"    s1 --> s2\n" +
				"    s1 --> s3\n" +
				"    classDef forwardOnly stroke-dasharray: 5 5\n" +
				"    class s2,s3 forwardOnly\n",
		},
		{
			name: "[Success] When: After lists parents out of declaration order ; Should: draw edges in After order",
			steps: []Option{
				Step("hotel", okAction),
				Step("flight", okAction),
				Step("car", okAction, After("flight", "hotel")),
			},
			want: "flowchart TD\n    s0[\"hotel\"]\n    s1[\"flight\"]\n    s2[\"car\"]\n" +
				"    s1 --> s2\n    s0 --> s2\n",
		},
		{
			name:  "[Success] When: a step name holds a quote ; Should: write the quote as #quot;",
			steps: []Option{Step(`say "hi"] x`, okAction)},
			want:  "flowchart TD\n    s0[\"say #quot;hi#quot;] x\"]\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := New[tripState]("trip", tt.steps...)
			require.NoError(t, err)

			assert.Equal(t, tt.want, s.Mermaid())
		})
	}
}
