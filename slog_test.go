package sagas

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Slog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		phase      Phase
		policy     RetryPolicy
		results    []error
		sleep      time.Duration
		wantLevels []string
	}{
		{
			name:       "[Success] When: the call succeeds ; Should: log one info record with every attribute",
			phase:      PhaseAction,
			results:    []error{nil},
			wantLevels: []string{"INFO"},
		},
		{
			name:       "[Fail] When: the call fails ; Should: log one warn record with the error",
			phase:      PhaseAction,
			results:    []error{Permanent(errCall)},
			wantLevels: []string{"WARN"},
		},
		{
			name:       "[Success] When: the first attempt fails ; Should: log attempts 1 and 2",
			phase:      PhaseCompensation,
			policy:     RetryPolicy{Attempts: 2, Initial: time.Millisecond, NoJitter: true},
			results:    []error{errCall, nil},
			wantLevels: []string{"WARN", "INFO"},
		},
		{
			name:       "[Success] When: the call sleeps three seconds ; Should: log a duration of three seconds",
			phase:      PhaseAction,
			results:    []error{nil},
			sleep:      3 * time.Second,
			wantLevels: []string{"INFO"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var out bytes.Buffer

				spec := &callSpec{
					saga:       "saga-1",
					step:       "step-1",
					phase:      tt.phase,
					retry:      tt.policy,
					middleware: []Middleware{Slog(slog.New(slog.NewJSONHandler(&out, nil)))},
				}

				attempt := 0
				call := func(context.Context, *pipeState) error {
					time.Sleep(tt.sleep)

					err := tt.results[attempt]
					attempt++

					return err
				}

				pipeline(spec, call)(t.Context(), "exec-1", baseOf(t, pipeState{}))

				lines := strings.Split(strings.TrimSpace(out.String()), "\n")
				require.Len(t, lines, len(tt.results))

				for i, line := range lines {
					var got map[string]any
					require.NoError(t, json.Unmarshal([]byte(line), &got))

					assert.Equal(t, tt.wantLevels[i], got["level"])
					assert.Equal(t, "saga-1", got["saga"])
					assert.Equal(t, "exec-1", got["execution"])
					assert.Equal(t, "step-1", got["step"])
					assert.Equal(t, string(tt.phase), got["phase"])
					assert.InDelta(t, float64(i+1), got["attempt"], 0)
					assert.InDelta(t, float64(tt.sleep), got["duration"], 0)

					wantKeys := 9
					if err := tt.results[i]; err != nil {
						wantKeys++

						assert.Equal(t, err.Error(), got["error"])
					}

					assert.Len(t, got, wantKeys)
				}
			})
		})
	}
}
