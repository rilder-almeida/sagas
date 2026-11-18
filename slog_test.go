package sagas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type slogAttempt struct {
	err       error
	wantLevel string
	wantText  string
}

func Test_Slog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		phase    Phase
		policy   RetryPolicy
		sleep    time.Duration
		attempts []slogAttempt
	}{
		{
			name:     "[Success] When: the call succeeds ; Should: log one info record with every attribute",
			phase:    PhaseAction,
			attempts: []slogAttempt{{wantLevel: "INFO"}},
		},
		{
			name:     "[Fail] When: the call fails ; Should: log one warn record with the error",
			phase:    PhaseAction,
			attempts: []slogAttempt{{err: Permanent(errCall), wantLevel: "WARN", wantText: "call failed"}},
		},
		{
			name:   "[Success] When: the first attempt fails ; Should: log attempts 1 and 2",
			phase:  PhaseCompensation,
			policy: RetryPolicy{Attempts: 2, Initial: time.Millisecond, NoJitter: true},
			attempts: []slogAttempt{
				{err: errCall, wantLevel: "WARN", wantText: "call failed"},
				{wantLevel: "INFO"},
			},
		},
		{
			name:     "[Success] When: the call sleeps three seconds ; Should: log a duration of three seconds",
			phase:    PhaseAction,
			sleep:    3 * time.Second,
			attempts: []slogAttempt{{wantLevel: "INFO"}},
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

					err := tt.attempts[attempt].err
					attempt++

					return err
				}

				got := pipeline(spec, call)(t.Context(), "exec-1", baseOf(t, pipeState{}))

				if last := tt.attempts[len(tt.attempts)-1].err; last == nil {
					require.NoError(t, got.err)
				} else {
					require.ErrorIs(t, got.err, last)
				}

				records := decodeRecords(t, &out)
				require.Len(t, records, len(tt.attempts))

				for i, rec := range records {
					assert.Equal(t, tt.attempts[i].wantLevel, rec["level"])
					assert.Equal(t, "saga-1", rec["saga"])
					assert.Equal(t, "exec-1", rec["execution"])
					assert.Equal(t, "step-1", rec["step"])
					assert.Equal(t, string(tt.phase), rec["phase"])
					assert.InDelta(t, float64(i+1), rec["attempt"], 0)
					assert.InDelta(t, float64(tt.sleep), rec["duration"], 0)
					assertErrorAttr(t, rec, tt.attempts[i].wantText)
				}
			})
		})
	}
}

func Test_Slog_panic(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	spec := &callSpec{
		saga:       "saga-1",
		step:       "step-1",
		phase:      PhaseAction,
		retry:      RetryPolicy{Attempts: 1},
		middleware: []Middleware{Slog(slog.New(slog.NewJSONHandler(&out, nil)))},
	}
	call := func(context.Context, *pipeState) error { panic("bad call") }

	got := pipeline(spec, call)(t.Context(), "exec-1", baseOf(t, pipeState{}))

	panicked, ok := errors.AsType[*PanicError](got.err)
	require.True(t, ok)
	assert.Equal(t, "bad call", panicked.Value)

	records := decodeRecords(t, &out)
	require.Len(t, records, 1)

	want := map[string]any{
		"level": "WARN", "saga": "saga-1", "execution": "exec-1", "step": "step-1", "phase": "action", "attempt": 1.0,
	}
	for key, value := range want {
		assert.Equal(t, value, records[0][key], key)
	}

	assertErrorAttr(t, records[0], "sagas: panic: bad call")
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
