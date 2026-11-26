package sagas

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) records(tb testing.TB, execution, level string) []map[string]any {
	tb.Helper()

	b.mu.Lock()
	lines := bytes.NewBufferString(b.buf.String())
	b.mu.Unlock()

	if lines.Len() == 0 {
		return nil
	}

	var found []map[string]any

	for _, rec := range decodeRecords(tb, lines) {
		if rec["execution"] == execution && rec["level"] == level {
			found = append(found, rec)
		}
	}

	return found
}

type capturedLog struct {
	slog.Handler

	out *lockedBuffer
}

func TestMain(m *testing.M) {
	out := &lockedBuffer{}
	slog.SetDefault(slog.New(&capturedLog{Handler: slog.NewJSONHandler(out, nil), out: out}))
	m.Run()
}

func defaultLog(tb testing.TB) *lockedBuffer {
	tb.Helper()

	captured, ok := slog.Default().Handler().(*capturedLog)
	require.True(tb, ok)

	return captured.out
}

const (
	ledgerName = "ledger"
	roomsName  = "rooms"
	cardName   = "card"
)

var errTipRefused = errors.New("sagas: test: the tip is refused")

type refusedTipStep struct{ Step[ledgerState, Tip] }

func (*refusedTipStep) Execute(context.Context, ledgerState) (Tip, error) {
	return 0, Permanent(errTipRefused)
}

func plainLedger() []Flow {
	return []Flow{&roomsStep{Name: roomsName, rooms: Rooms{"101"}}, &cardStep{Name: cardName, card: "4111"}}
}

func refundedLedger() []Flow {
	return []Flow{
		&roomsStep{Name: roomsName, rooms: Rooms{"101"}},
		&refundCardStep{cardStep{Name: cardName, card: "4111"}},
		&refusedTipStep{Step[ledgerState, Tip]{Name: "refused-tip"}},
	}
}

func ledgerOf(tb testing.TB, name string, steps []Flow, opts ...Option) *Saga[ledgerState] {
	tb.Helper()

	s, err := New[ledgerState](name, Sequence(steps...), opts...)
	require.NoError(tb, err)

	return s
}

func namedLedger(tb testing.TB, name string, opts ...Option) *Saga[ledgerState] {
	tb.Helper()

	return ledgerOf(tb, name, plainLedger(), opts...)
}

func journalEvents(tb testing.TB, j EventReader, execution string) []Event {
	tb.Helper()

	events, err := j.Events(tb.Context(), execution)
	require.NoError(tb, err)

	return events
}

func runTimed(tb testing.TB, s *Saga[ledgerState], execution string) (time.Duration, error) {
	tb.Helper()

	start := time.Now()
	err := s.Run(tb.Context(), &ledgerState{ID: execution})

	return time.Since(start), err
}

func Test_sagaDeliverer_Observe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		panics        bool
		scribble      bool
		wantFirstSees bool
		wantPanicLogs bool
	}{
		{
			name:   "[Fail] When: the first observer panics on each event ; Should: log each panic, feed the second",
			panics: true, wantPanicLogs: true,
		},
		{
			name:     "[Success] When: the first observer changes its copy ; Should: give the second an unchanged copy",
			scribble: true, wantFirstSees: true,
		},
		{
			name:          "[Success] When: both observers only read ; Should: give both every event and log nothing",
			wantFirstSees: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				q := NewMemoryEventQueue()
				journal := NewMemoryJournal()
				first := &eventLog{panics: tt.panics, scribble: tt.scribble, delay: time.Second}
				second := &eventLog{}

				s := namedLedger(t, ledgerName, WithJournal(journal), WithEventQueue(q), WithObserver(first, second))

				execution := newID()

				elapsed, runErr := runTimed(t, s, execution)
				require.NoError(t, runErr)
				require.NoError(t, q.Close(t.Context()))

				want := journalEvents(t, journal, execution)

				var wantFirst, wantPanics []Event

				if tt.wantFirstSees {
					wantFirst = want
				}

				if tt.wantPanicLogs {
					wantPanics = want
				}

				assert.Zero(t, elapsed)
				assert.Equal(t, want, second.seen())
				assert.Equal(t, wantFirst, first.seen())
				assertPanicLogs(t, defaultLog(t).records(t, execution, slog.LevelError.String()), wantPanics)
			})
		})
	}
}
