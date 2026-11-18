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

func decodeRecords(tb testing.TB, out *bytes.Buffer) []map[string]any {
	tb.Helper()

	var records []map[string]any

	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		var rec map[string]any

		require.NoError(tb, json.Unmarshal([]byte(line), &rec))

		records = append(records, rec)
	}

	return records
}

func requireLastErr(tb testing.TB, got error, results []error) {
	tb.Helper()

	last := results[len(results)-1]
	if last == nil {
		require.NoError(tb, got)

		return
	}

	require.ErrorIs(tb, got, last)
}

func Test_Slog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		direct     bool
		phase      Phase
		policy     RetryPolicy
		results    []error
		sleep      time.Duration
		wantLevels []string
	}{
		{
			name:       "[Fail] When: called outside a pipeline and fails ; Should: return the error and log no step",
			direct:     true,
			results:    []error{errCall},
			wantLevels: []string{"WARN"},
		},
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

				logger := slog.New(slog.NewJSONHandler(&out, nil))

				attempt := 0
				run := func(context.Context) error {
					time.Sleep(tt.sleep)

					err := tt.results[attempt]
					attempt++

					return err
				}

				var gotErr error

				wantSaga, wantExecution, wantStep, firstAttempt := "saga-1", "exec-1", "step-1", 1

				if tt.direct {
					wantSaga, wantExecution, wantStep, firstAttempt = "", "", "", 0
					gotErr = Slog(logger)(run)(t.Context())
				} else {
					spec := &callSpec{
						saga:       wantSaga,
						step:       wantStep,
						phase:      tt.phase,
						retry:      tt.policy,
						middleware: []Middleware{Slog(logger)},
					}
					call := func(ctx context.Context, _ *pipeState) error { return run(ctx) }

					gotErr = pipeline(spec, call)(t.Context(), wantExecution, baseOf(t, pipeState{})).err
				}

				requireLastErr(t, gotErr, tt.results)

				records := decodeRecords(t, &out)
				require.Len(t, records, len(tt.results))

				for i, got := range records {
					assert.Equal(t, tt.wantLevels[i], got["level"])
					assert.Equal(t, wantSaga, got["saga"])
					assert.Equal(t, wantExecution, got["execution"])
					assert.Equal(t, wantStep, got["step"])
					assert.Equal(t, string(tt.phase), got["phase"])
					assert.InDelta(t, float64(firstAttempt+i), got["attempt"], 0)
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
