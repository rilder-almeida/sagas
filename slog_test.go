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

func requireErrorText(tb testing.TB, err error, want string) {
	tb.Helper()

	if want == "" {
		require.NoError(tb, err)

		return
	}

	require.ErrorContains(tb, err, want)
}

func assertErrorAttr(tb testing.TB, rec map[string]any, want string) {
	tb.Helper()

	const baseKeys = 9

	if want == "" {
		assert.Len(tb, rec, baseKeys)

		return
	}

	assert.Len(tb, rec, baseKeys+1)
	assert.Contains(tb, rec["error"], want)
}

func Test_Slog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		phase      Phase
		policy     RetryPolicy
		results    []error
		panicValue any
		sleep      time.Duration
		wantLevels []string
		wantErrors []string
	}{
		{
			name:       "[Success] When: the call succeeds ; Should: log one info record with every attribute",
			phase:      PhaseAction,
			results:    []error{nil},
			wantLevels: []string{"INFO"},
			wantErrors: []string{""},
		},
		{
			name:       "[Fail] When: the call fails ; Should: log one warn record with the error",
			phase:      PhaseAction,
			results:    []error{Permanent(errCall)},
			wantLevels: []string{"WARN"},
			wantErrors: []string{"call failed"},
		},
		{
			name:       "[Success] When: the first attempt fails ; Should: log attempts 1 and 2",
			phase:      PhaseCompensation,
			policy:     RetryPolicy{Attempts: 2, Initial: time.Millisecond, NoJitter: true},
			results:    []error{errCall, nil},
			wantLevels: []string{"WARN", "INFO"},
			wantErrors: []string{"call failed", ""},
		},
		{
			name:       "[Success] When: the call sleeps three seconds ; Should: log a duration of three seconds",
			phase:      PhaseAction,
			results:    []error{nil},
			sleep:      3 * time.Second,
			wantLevels: []string{"INFO"},
			wantErrors: []string{""},
		},
		{
			name:       "[Fail] When: the call panics ; Should: log one warn record with the panic value",
			phase:      PhaseAction,
			panicValue: "bad call",
			wantLevels: []string{"WARN"},
			wantErrors: []string{"sagas: panic: bad call"},
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

					if tt.panicValue != nil {
						panic(tt.panicValue)
					}

					err := tt.results[attempt]
					attempt++

					return err
				}

				got := pipeline(spec, call)(t.Context(), "exec-1", baseOf(t, pipeState{}))

				requireErrorText(t, got.err, tt.wantErrors[len(tt.wantErrors)-1])

				records := decodeRecords(t, &out)
				require.Len(t, records, len(tt.wantLevels))

				for i, rec := range records {
					assert.Equal(t, tt.wantLevels[i], rec["level"])
					assert.Equal(t, "saga-1", rec["saga"])
					assert.Equal(t, "exec-1", rec["execution"])
					assert.Equal(t, "step-1", rec["step"])
					assert.Equal(t, string(tt.phase), rec["phase"])
					assert.InDelta(t, float64(i+1), rec["attempt"], 0)
					assert.InDelta(t, float64(tt.sleep), rec["duration"], 0)

					assertErrorAttr(t, rec, tt.wantErrors[i])
				}
			})
		})
	}
}

func Test_Slog_direct(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	h := func(context.Context) error { return errCall }

	err := Slog(slog.New(slog.NewJSONHandler(&out, nil)))(h)(t.Context())

	require.ErrorIs(t, err, errCall)

	records := decodeRecords(t, &out)
	require.Len(t, records, 1)

	rec := records[0]
	assert.Equal(t, "WARN", rec["level"])

	for _, key := range []string{"saga", "execution", "step", "phase"} {
		assert.Contains(t, rec, key)
		assert.Empty(t, rec[key])
	}

	assert.InDelta(t, 0, rec["attempt"], 0)
	assert.Equal(t, "call failed", rec["error"])
}
