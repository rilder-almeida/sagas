package sagas

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type markKey struct{}

const markValue = "mark"

var errStopWaiting = errors.New("sagas: test: stop waiting")

var errObserverPanic = errors.New("sagas: test: the observer panics")

type eventLog struct {
	hold     chan struct{}
	delay    time.Duration
	panics   bool
	scribble bool

	mu      sync.Mutex
	events  []Event
	ctxErrs []error
	marks   []any
}

func heldLog() *eventLog { return &eventLog{hold: make(chan struct{})} }

func (l *eventLog) Observe(ctx context.Context, e *Event) {
	if l.hold != nil {
		<-l.hold
	}

	time.Sleep(l.delay)

	if l.panics {
		panic(errObserverPanic)
	}

	own := *e
	cloneSlices(&own)

	if l.scribble {
		for _, v := range e.Values {
			clear(v.Value)
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.events = append(l.events, own)
	l.ctxErrs = append(l.ctxErrs, ctx.Err())
	l.marks = append(l.marks, ctx.Value(markKey{}))
}

func (l *eventLog) release() {
	if l.hold != nil {
		close(l.hold)
	}
}

func (l *eventLog) seen() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.events
}

func (l *eventLog) seqs() []int {
	seen := l.seen()
	if len(seen) == 0 {
		return nil
	}

	seqs := make([]int, len(seen))
	for i := range seen {
		seqs[i] = seen[i].Seq
	}

	return seqs
}

func seqsUpTo(n int) []int {
	if n == 0 {
		return nil
	}

	seqs := make([]int, n)
	for i := range seqs {
		seqs[i] = i + 1
	}

	return seqs
}

func (l *eventLog) checkDeliveries(tb testing.TB, want int) {
	tb.Helper()

	l.mu.Lock()
	defer l.mu.Unlock()

	assert.Len(tb, l.marks, want)

	for i, mark := range l.marks {
		require.NoError(tb, l.ctxErrs[i])
		assert.Equal(tb, markValue, mark)
		assert.Equal(tb, sentEvent(i+1).Values, l.events[i].Values)
	}
}

const sentValue = `1`

func sentEvent(seq int) *Event {
	return &Event{Seq: seq, Values: []FieldValue{{Field: "Fee", Value: jsontext.Value(sentValue)}}}
}

func enqueueAll(tb testing.TB, q *MemoryEventQueue, sent int, wantErr error) (int, []*Event) {
	tb.Helper()

	ctx, cancel := context.WithCancel(context.WithValue(tb.Context(), markKey{}, markValue))
	cancel()

	accepted := 0
	events := make([]*Event, 0, sent)

	for i := range sent {
		e := sentEvent(i + 1)
		events = append(events, e)

		err := q.Enqueue(ctx, e)
		if err == nil {
			accepted++
		} else {
			require.ErrorIs(tb, err, wantErr)
		}

		synctest.Wait()
	}

	return accepted, events
}

func change(events []*Event) {
	for _, e := range events {
		e.Seq += 100
		e.Values[0].Value[0] = '9'
	}
}

func assertCapacity(t *testing.T, opts []QueueOption, want int) {
	t.Helper()

	synctest.Test(t, func(t *testing.T) {
		q := NewMemoryEventQueue(opts...)
		log := heldLog()
		q.Subscribe(log)

		require.NoError(t, q.Enqueue(t.Context(), &Event{Seq: 1}))
		synctest.Wait()

		held := 0
		for held <= want && q.Enqueue(t.Context(), &Event{Seq: held + 2}) == nil {
			held++
		}

		log.release()
		require.NoError(t, q.Close(t.Context()))

		assert.Equal(t, want, held)
		assert.Equal(t, seqsUpTo(want+1), log.seqs())
	})
}

func Test_NewMemoryEventQueue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		opts         []QueueOption
		wantCapacity int
	}{
		{
			name:         "[Success] When: no option is given ; Should: hold DefaultEventQueueSize events",
			wantCapacity: DefaultEventQueueSize,
		},
		{
			name: "[Success] When: an option is nil ; Should: skip it and apply the others",
			opts: []QueueOption{nil, WithQueueSize(4)}, wantCapacity: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assertCapacity(t, tt.opts, tt.wantCapacity)
		})
	}
}

func Test_WithQueueSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		sizes        []int
		wantCapacity int
	}{
		{
			name:  "[Success] When: the size is zero ; Should: hold DefaultEventQueueSize events",
			sizes: []int{0}, wantCapacity: DefaultEventQueueSize,
		},
		{
			name:  "[Success] When: the size is negative ; Should: hold DefaultEventQueueSize events",
			sizes: []int{-3}, wantCapacity: DefaultEventQueueSize,
		},
		{
			name:  "[Success] When: the size is four ; Should: hold four events",
			sizes: []int{4}, wantCapacity: 4,
		},
		{
			name:  "[Success] When: the option is given twice ; Should: keep the last size",
			sizes: []int{2, 4}, wantCapacity: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := make([]QueueOption, 0, len(tt.sizes))
			for _, n := range tt.sizes {
				opts = append(opts, WithQueueSize(n))
			}

			assertCapacity(t, opts, tt.wantCapacity)
		})
	}
}

func Test_MemoryEventQueue_Enqueue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		size         int
		hold         bool
		closeFirst   bool
		changeAfter  bool
		sent         int
		wantAccepted int
		wantErr      error
	}{
		{
			name: "[Success] When: two subscribers and three events ; Should: deliver the three in order to both",
			sent: 3, wantAccepted: 3,
		},
		{
			name: "[Fail] When: a queue of size 1 is full ; Should: return an error at once and keep the others",
			size: 1, hold: true, sent: 3, wantAccepted: 2, wantErr: errQueueFull,
		},
		{
			name: "[Success] When: the caller changes its events before delivery ; Should: deliver the events as sent",
			hold: true, changeAfter: true, sent: 2, wantAccepted: 2,
		},
		{
			name:       "[Fail] When: the queue is closed ; Should: return an error and deliver nothing",
			closeFirst: true, sent: 2, wantAccepted: 0, wantErr: errQueueClosed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				q := NewMemoryEventQueue(WithQueueSize(tt.size))

				first, second := &eventLog{}, &eventLog{}
				if tt.hold {
					first = heldLog()
				}

				q.Subscribe(first)
				q.Subscribe(second)

				if tt.closeFirst {
					require.NoError(t, q.Close(t.Context()))
				}

				accepted, sent := enqueueAll(t, q, tt.sent, tt.wantErr)
				if tt.changeAfter {
					change(sent)
				}

				first.release()
				require.NoError(t, q.Close(t.Context()))

				assert.Equal(t, tt.wantAccepted, accepted)
				assert.Equal(t, seqsUpTo(tt.wantAccepted), first.seqs())
				assert.Equal(t, seqsUpTo(tt.wantAccepted), second.seqs())

				first.checkDeliveries(t, tt.wantAccepted)
				second.checkDeliveries(t, tt.wantAccepted)
			})
		})
	}
}

func Test_MemoryEventQueue_Subscribe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		early int
		late  int
	}{
		{name: "[Success] When: one subscriber comes before the events ; Should: get both events", early: 1},
		{
			name:  "[Success] When: one subscriber comes between two events ; Should: get only the second event",
			early: 2, late: 1,
		},
		{name: "[Success] When: no subscriber comes before the first event ; Should: drop it for nobody", late: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				q := NewMemoryEventQueue()

				subscribe := func(n int) []*eventLog {
					logs := make([]*eventLog, n)
					for i := range logs {
						logs[i] = &eventLog{}
						q.Subscribe(logs[i])
					}

					return logs
				}

				early := subscribe(tt.early)

				require.NoError(t, q.Enqueue(t.Context(), &Event{Seq: 1}))
				synctest.Wait()

				late := subscribe(tt.late)

				require.NoError(t, q.Enqueue(t.Context(), &Event{Seq: 2}))
				require.NoError(t, q.Close(t.Context()))

				for _, log := range early {
					assert.Equal(t, []int{1, 2}, log.seqs())
				}

				for _, log := range late {
					assert.Equal(t, []int{2}, log.seqs())
				}
			})
		})
	}
}

func Test_MemoryEventQueue_Close(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		hold        bool
		delay       time.Duration
		timeout     time.Duration
		wantErr     error
		wantSeen    []int
		wantElapsed time.Duration
	}{
		{
			name:  "[Success] When: each event takes one second ; Should: return nil after both are delivered",
			delay: time.Second, wantSeen: []int{1, 2}, wantElapsed: 2 * time.Second,
		},
		{
			name: "[Fail] When: ctx ends while the subscriber blocks ; Should: return the cause of ctx",
			hold: true, timeout: 5 * time.Second, wantErr: errStopWaiting, wantElapsed: 5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				q := NewMemoryEventQueue()

				log := &eventLog{delay: tt.delay}
				if tt.hold {
					log.hold = make(chan struct{})
				}

				q.Subscribe(log)

				require.NoError(t, q.Enqueue(t.Context(), &Event{Seq: 1}))
				require.NoError(t, q.Enqueue(t.Context(), &Event{Seq: 2}))

				ctx := t.Context()

				if tt.timeout > 0 {
					var cancel context.CancelFunc

					ctx, cancel = context.WithTimeoutCause(ctx, tt.timeout, errStopWaiting)
					defer cancel()
				}

				start := time.Now()
				err := q.Close(ctx)
				elapsed := time.Since(start)
				seen := log.seqs()

				log.release()
				require.NoError(t, q.Close(t.Context()))

				require.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, tt.wantSeen, seen)
				assert.Equal(t, tt.wantElapsed, elapsed)
			})
		})
	}
}
