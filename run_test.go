package sagas

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type floatState struct {
	F float64
}

var hexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func Test_WithExecutionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		ids           []string
		wantExecution string
		wantGiven     int
	}{
		{
			name:          "[Success] When: the option is applied once ; Should: set the execution and count it once",
			ids:           []string{"order-42"},
			wantExecution: "order-42",
			wantGiven:     1,
		},
		{
			name:          "[Success] When: the option is applied twice ; Should: keep the last one and count two",
			ids:           []string{"order-42", "order-43"},
			wantExecution: "order-43",
			wantGiven:     2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := runConfig{given: map[string]int{}}
			for _, id := range tt.ids {
				WithExecutionID(id)(&cfg)
			}

			assert.Equal(t, tt.wantExecution, cfg.execution)
			assert.Equal(t, tt.wantGiven, cfg.given["WithExecutionID"])
		})
	}
}

func Test_StepError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  StepError
		want string
	}{
		{
			name: "[Success] When: an action failed ; Should: name the step and its attempts",
			err:  StepError{Step: payStep, Attempts: 3, Err: errAlwaysFails},
			want: `sagas: step "pay" failed after 3 attempts: ` + errAlwaysFails.Error(),
		},
		{
			name: "[Success] When: a compensation failed ; Should: name the compensation of the step",
			err:  StepError{Step: payStep, Attempts: 1, Compensation: true, Err: errAlwaysFails},
			want: `sagas: compensation of step "pay" failed after 1 attempts: ` + errAlwaysFails.Error(),
		},
		{
			name: "[Success] When: the step never ran ; Should: report 0 attempts",
			err:  StepError{Step: "car", Err: ErrJournal},
			want: `sagas: step "car" failed after 0 attempts: ` + ErrJournal.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

func Test_StepError_Unwrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		wantIs bool
	}{
		{
			name:   "[Success] When: Err is the sentinel ; Should: match it through errors.Is",
			err:    errAlwaysFails,
			wantIs: true,
		},
		{
			name:   "[Success] When: Err wraps the sentinel ; Should: match it through the chain",
			err:    fmt.Errorf("booking: %w", errAlwaysFails),
			wantIs: true,
		},
		{
			name:   "[Fail] When: Err is nil ; Should: match nothing",
			err:    nil,
			wantIs: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			se := &StepError{Step: payStep, Err: tt.err}

			require.Equal(t, tt.err, se.Unwrap())
			assert.Equal(t, tt.wantIs, errors.Is(se, errAlwaysFails))
		})
	}
}

func Test_checkRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		run           func() (runConfig, error)
		wantExecution *regexp.Regexp
		wantErrs      []error
		wantContains  []string
	}{
		{
			name:          "[Success] When: no option is given ; Should: draw a random 32-char hex execution",
			run:           func() (runConfig, error) { return checkRun(&tripState{}, nil) },
			wantExecution: hexID,
		},
		{
			name: "[Success] When: WithExecutionID is valid ; Should: keep the given execution",
			run: func() (runConfig, error) {
				return checkRun(&tripState{}, []RunOption{WithExecutionID("order-42")})
			},
			wantExecution: regexp.MustCompile(`^order-42$`),
		},
		{
			name:         "[Fail] When: an option is nil ; Should: return ErrInvalidRun",
			run:          func() (runConfig, error) { return checkRun(&tripState{}, []RunOption{nil}) },
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{"nil RunOption"},
		},
		{
			name: "[Fail] When: WithExecutionID is given twice ; Should: return ErrInvalidRun",
			run: func() (runConfig, error) {
				return checkRun(&tripState{}, twice(WithExecutionID("order-42")))
			},
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{"WithExecutionID given more than once"},
		},
		{
			name: "[Fail] When: the execution ID is empty ; Should: return ErrInvalidRun",
			run: func() (runConfig, error) {
				return checkRun(&tripState{}, []RunOption{WithExecutionID("")})
			},
			wantErrs:     []error{ErrInvalidRun},
			wantContains: checkName(""),
		},
		{
			name: "[Fail] When: the execution ID holds a colon ; Should: return ErrInvalidRun",
			run: func() (runConfig, error) {
				return checkRun(&tripState{}, []RunOption{WithExecutionID("order:42")})
			},
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{nameRule},
		},
		{
			name: "[Fail] When: the execution ID starts with a dash ; Should: return ErrInvalidRun",
			run: func() (runConfig, error) {
				return checkRun(&tripState{}, []RunOption{WithExecutionID("-x")})
			},
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{nameRule},
		},
		{
			name:         "[Fail] When: s is nil ; Should: return ErrInvalidState",
			run:          func() (runConfig, error) { return checkRun[tripState](nil, nil) },
			wantErrs:     []error{ErrInvalidState},
			wantContains: []string{"state is nil"},
		},
		{
			name:         "[Fail] When: the initial state holds NaN ; Should: return ErrInvalidState",
			run:          func() (runConfig, error) { return checkRun(&floatState{F: math.NaN()}, nil) },
			wantErrs:     []error{ErrInvalidState},
			wantContains: []string{"F"},
		},
		{
			name:         "[Fail] When: an option and s are both bad ; Should: join both errors",
			run:          func() (runConfig, error) { return checkRun[tripState](nil, []RunOption{nil}) },
			wantErrs:     []error{ErrInvalidRun, ErrInvalidState},
			wantContains: []string{"nil RunOption", "state is nil"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := tt.run()
			if len(tt.wantErrs) > 0 {
				for _, want := range tt.wantErrs {
					require.ErrorIs(t, err, want)
				}

				requireText(t, err, tt.wantContains, nil)
				assert.Empty(t, cfg.execution)

				return
			}

			require.NoError(t, err)
			assert.Regexp(t, tt.wantExecution, cfg.execution)
			assert.Empty(t, checkName(cfg.execution))
		})
	}
}

func Test_newID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		calls int
	}{
		{name: "[Success] When: called twice ; Should: return two different 32-char hex IDs", calls: 2},
		{name: "[Success] When: called a hundred times ; Should: never repeat an ID", calls: 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			seen := map[string]bool{}

			for range tt.calls {
				id := newID()
				assert.Regexp(t, hexID, id)
				assert.False(t, seen[id], id)

				seen[id] = true
			}

			assert.Len(t, seen, tt.calls)
		})
	}
}
