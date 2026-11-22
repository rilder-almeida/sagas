package sagas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Saga_Mermaid(t *testing.T) {
	t.Parallel()

	hotel, flight, receipt, mail := act("hotel", okAction), act("flight", okAction), act("receipt", okAction),
		act("mail", okAction)
	car, pay := gated("car", okAction, okPredicate), paying("pay", okAction)
	described := act("hotel", okAction)
	described.Description = "books the room"

	tests := []struct {
		name string
		flow Flow
		want string
	}{
		{
			name: "[Success] When: the saga is the book-trip example ; Should: draw nodes, edges and the class",
			flow: Sequence(Parallel(hotel, flight, car), pay, receipt),
			want: "flowchart TD\n" +
				"    s0[\"hotel\"]\n" +
				"    s1[\"flight\"]\n" +
				"    s2[\"car (if Enabled)\"]\n" +
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
			name: "[Success] When: the saga has one step ; Should: draw one node and no edge",
			flow: hotel,
			want: "flowchart TD\n    s0[\"hotel\"]\n",
		},
		{
			name: "[Success] When: the saga has no pivot ; Should: draw the edges and no class lines",
			flow: Sequence(hotel, flight),
			want: "flowchart TD\n    s0[\"hotel\"]\n    s1[\"flight\"]\n    s0 --> s1\n",
		},
		{
			name: "[Success] When: two steps are forward-only ; Should: list both ids in one class line",
			flow: Sequence(hotel, pay, Parallel(receipt, mail)),
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
			name: "[Success] When: the flow links a step to later steps first ; Should: draw edges in flow order",
			flow: Parallel(hotel, Sequence(flight, receipt), Sequence(hotel, receipt)),
			want: "flowchart TD\n    s0[\"hotel\"]\n    s1[\"flight\"]\n    s2[\"receipt\"]\n" +
				"    s1 --> s2\n    s0 --> s2\n",
		},
		{
			name: "[Success] When: a step name holds a dash, an underscore and a dot ; Should: write it unchanged",
			flow: act("book-trip_v2.1", okAction),
			want: "flowchart TD\n    s0[\"book-trip_v2.1\"]\n",
		},
		{
			name: "[Success] When: a step has a description ; Should: leave the description out",
			flow: described,
			want: "flowchart TD\n    s0[\"hotel\"]\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := New[tripState]("trip", tt.flow)
			require.NoError(t, err)

			assert.Equal(t, tt.want, s.Mermaid())
		})
	}
}
