package sagas

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_plan_add(t *testing.T) {
	t.Parallel()

	action := NewAction(func(ctx context.Context) error { return nil })

	type args struct {
		identifier Identifier
		event      Event
	}

	tests := []struct {
		name string
		args args
		want plan
	}{
		{
			name: "[SUCCESS] Should add an action to the plan",
			args: args{
				identifier: identifier("identifier"),
				event:      Completed,
			},
			want: plan{
				identifier("identifier"): {
					Completed: []Action{action},
				},
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				p := newPlan()
				p.add(test.args.identifier, test.args.event, action)
				got := p
				assert.Equal(t, test.want, got)
			})
		})
	}
}

func Test_plan_get(t *testing.T) {
	t.Parallel()

	action := NewAction(func(ctx context.Context) error { return nil })
	p := newPlan()
	p.add(identifier("identifier"), Completed, action)

	type args struct {
		identifier Identifier
		event      Event
	}

	tests := []struct {
		name        string
		args        args
		wantActions []Action
		wantOk      bool
	}{
		{
			name: "[SUCCESS] Should return a list of actions and true",
			args: args{
				identifier: identifier("identifier"),
				event:      Completed,
			},
			wantActions: []Action{action},
			wantOk:      true,
		},

		{
			name: "[SUCCESS] Should return a nil list of actions and false - identifier does not exist",
			args: args{
				identifier: identifier("not-exists"),
				event:      Completed,
			},
			wantActions: nil,
			wantOk:      false,
		},

		{
			name: "[SUCCESS] Should return a nil list of actions and false - event does not exist",
			args: args{
				identifier: identifier("identifier"),
				event:      Successed,
			},
			wantActions: nil,
			wantOk:      false,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				got, ok := p.get(test.args.identifier, test.args.event)
				assert.Equal(t, test.wantActions, got)
				assert.Equal(t, test.wantOk, ok)
			})
		})
	}
}
