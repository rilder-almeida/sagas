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

const (
	levelInfo = "INFO"
	levelWarn = "WARN"

	callFailedText = "call failed"
)

type slogAttempt struct {
	err         error
	panicValue  any
	wantLevel   string
	wantAttempt int
	wantText    string
}

func slogCall(attempts []slogAttempt, sleep time.Duration) func(context.Context, *pipeState) error {
	n := 0

	return func(context.Context, *pipeState) error {
		time.Sleep(sleep)

		a := attempts[n]
		n++

		if a.panicValue != nil {
			panic(a.panicValue)
		}

		return a.err
	}
}

func Test_Slog_Wrap(t *testing.T) {
	t.Parallel()

	inCall := func(phase Phase) CallInfo {
		return CallInfo{Saga: "saga-1", Execution: infoExecution, Step: "step-1", Phase: phase}
	}

	tests := []struct {
		name      string
		direct    bool
		info      CallInfo
		wantPhase string
		policy    RetryPolicy
		sleep     time.Duration
		attempts  []slogAttempt
		wantErr   error
	}{
		{
			name:      "[Success] When: the call succeeds ; Should: log one info record with every attribute",
			info:      inCall(PhaseExecute),
			wantPhase: string(PhaseExecute),
			attempts:  []slogAttempt{{wantLevel: levelInfo, wantAttempt: 1}},
		},
		{
			name:      "[Fail] When: the call fails ; Should: log one warn record with the error",
			info:      inCall(PhaseExecute),
			wantPhase: string(PhaseExecute),
			attempts: []slogAttempt{
				{err: Permanent(errCall), wantLevel: levelWarn, wantAttempt: 1, wantText: callFailedText},
			},
			wantErr: errCall,
		},
		{
			name:      "[Success] When: a pivot call succeeds ; Should: log the phase pivot",
			info:      inCall(PhasePivot),
			wantPhase: string(PhasePivot),
			attempts:  []slogAttempt{{wantLevel: levelInfo, wantAttempt: 1}},
		},
		{
			name:      "[Success] When: the first compensate attempt fails ; Should: log attempts 1 and 2",
			info:      inCall(PhaseCompensate),
			wantPhase: string(PhaseCompensate),
			policy:    RetryPolicy{Attempts: 2, Initial: time.Millisecond, NoJitter: true},
			attempts: []slogAttempt{
				{err: errCall, wantLevel: levelWarn, wantAttempt: 1, wantText: callFailedText},
				{wantLevel: levelInfo, wantAttempt: 2},
			},
		},
		{
			name:      "[Success] When: the call sleeps three seconds ; Should: log a duration of three seconds",
			info:      inCall(PhaseExecute),
			wantPhase: string(PhaseExecute),
			sleep:     3 * time.Second,
			attempts:  []slogAttempt{{wantLevel: levelInfo, wantAttempt: 1}},
		},
		{
			name:      "[Fail] When: the call panics ; Should: log one warn record with the panic error",
			info:      inCall(PhaseExecute),
			wantPhase: string(PhaseExecute),
			policy:    RetryPolicy{Attempts: 1},
			attempts: []slogAttempt{
				{panicValue: errBoom, wantLevel: levelWarn, wantAttempt: 1, wantText: "sagas: panic: boom"},
			},
			wantErr: errBoom,
		},
		{
			name:      "[Fail] When: Wrap runs outside a pipeline call ; Should: log empty call fields and the error",
			direct:    true,
			info:      CallInfo{},
			wantPhase: "",
			attempts:  []slogAttempt{{err: errCall, wantLevel: levelWarn, wantAttempt: 0, wantText: callFailedText}},
			wantErr:   errCall,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var out bytes.Buffer

				mw := Slog{Logger: slog.New(slog.NewJSONHandler(&out, nil))}
				call := slogCall(tt.attempts, tt.sleep)

				var err error

				if tt.direct {
					err = mw.Wrap(t.Context(), tt.info, func(ctx context.Context) error { return call(ctx, nil) })
				} else {
					spec := &callSpec{
						saga: tt.info.Saga, step: tt.info.Step, phase: tt.info.Phase, retry: tt.policy,
						middleware: []Middleware{mw},
					}
					meta := &callMeta{execution: tt.info.Execution}
					err = pipeline(spec, call)(t.Context(), meta, baseOf(t, pipeState{})).err
				}

				assertSlogErr(t, err, tt.wantErr)

				records := decodeRecords(t, &out)
				require.Len(t, records, len(tt.attempts))

				for i, rec := range records {
					assert.Equal(t, tt.attempts[i].wantLevel, rec["level"])
					assert.Equal(t, tt.info.Saga, rec["saga"])
					assert.Equal(t, tt.info.Execution, rec["execution"])
					assert.Equal(t, tt.info.Step, rec["step"])
					assert.Equal(t, tt.wantPhase, rec["phase"])
					assert.InDelta(t, float64(tt.attempts[i].wantAttempt), rec["attempt"], 0)
					assert.InDelta(t, float64(tt.sleep), rec["duration"], 0)
					assertErrorAttr(t, rec, tt.attempts[i].wantText)
				}
			})
		})
	}
}

func Test_Slog_Wrap_defaultLogger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		info      CallInfo
		callErr   error
		wantLevel string
		wantText  string
	}{
		{
			name:      "[Fail] When: Logger is nil and the call fails ; Should: log one warn record to slog.Default",
			info:      CallInfo{Saga: "saga-1", Step: "step-1"},
			callErr:   errCall,
			wantLevel: levelWarn,
			wantText:  callFailedText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			previous := slog.Default()

			slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })

			err := Slog{}.Wrap(t.Context(), tt.info, func(context.Context) error { return tt.callErr })
			require.ErrorIs(t, err, tt.callErr)

			records := decodeRecords(t, &out)
			require.Len(t, records, 1)
			assert.Equal(t, tt.wantLevel, records[0]["level"])
			assert.Equal(t, tt.info.Saga, records[0]["saga"])
			assert.Equal(t, tt.info.Step, records[0]["step"])
			assertErrorAttr(t, records[0], tt.wantText)
		})
	}
}

func assertSlogErr(tb testing.TB, err, want error) {
	tb.Helper()

	if want == nil {
		require.NoError(tb, err)

		return
	}

	require.ErrorIs(tb, err, want)
}
