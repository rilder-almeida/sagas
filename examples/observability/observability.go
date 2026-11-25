package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/rilder-almeida/sagas"
)

const (
	exitFailure      = 1
	exitWriteFailure = 2
)

// --8<-- [start:state]

type order struct {
	ID       string `sagas:"id"`
	StockID  string
	ChargeID string
}

// --8<-- [end:state]

// --8<-- [start:provider]

var errBusy = errors.New("provider busy")

type provider struct {
	mu   sync.Mutex
	seen map[string]bool
}

func newProvider() *provider { return &provider{seen: map[string]bool{}} }

func (p *provider) charge(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.seen[key] {
		p.seen[key] = true

		return errBusy
	}

	return nil
}

// --8<-- [end:provider]

// --8<-- [start:steps]

type reserve struct{ sagas.Step }

func (reserve) Execute(ctx context.Context, o *order) error {
	o.StockID = sagas.IdempotencyKey(ctx)

	return ctx.Err()
}

type charge struct {
	sagas.Step

	provider *provider
}

func (c *charge) Execute(ctx context.Context, o *order) error {
	key := sagas.IdempotencyKey(ctx)
	if err := c.provider.charge(key); err != nil {
		return err
	}

	o.ChargeID = key

	return ctx.Err()
}

// --8<-- [end:steps]

// --8<-- [start:slog]

func newLogger(w io.Writer) *slog.Logger {
	drop := func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey || a.Key == "duration" {
			return slog.Attr{}
		}

		return a
	}

	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: drop}))
}

// --8<-- [end:slog]

// --8<-- [start:keylog]

type keyLog struct {
	logger *slog.Logger
}

func (k keyLog) Wrap(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
	keyed := k.logger.With("idempotency_key", info.IdempotencyKey())

	return sagas.Slog{Logger: keyed}.Wrap(ctx, info, call)
}

// --8<-- [end:keylog]

// --8<-- [start:saga]

const (
	attempts   = 2
	retryDelay = time.Millisecond
)

func newSaga(logger *slog.Logger) (*sagas.Saga[order], error) {
	retry := sagas.RetryPolicy{Attempts: attempts, Initial: retryDelay, NoJitter: true}
	flow := sagas.Sequence(&reserve{}, &charge{RetryPolicy: retry, provider: newProvider()})

	return sagas.New[order]("order", flow, sagas.WithMiddleware(keyLog{logger: logger}))
}

// --8<-- [end:saga]

// --8<-- [start:run]

type watchedWriter struct {
	w io.Writer

	mu  sync.Mutex
	err error
}

func (w *watchedWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if err != nil {
		w.mu.Lock()
		w.err = errors.Join(w.err, err)
		w.mu.Unlock()
	}

	return n, err
}

func (w *watchedWriter) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.err
}

func run(w io.Writer) error {
	out := &watchedWriter{w: w}

	saga, err := newSaga(newLogger(out))
	if err != nil {
		return err
	}

	runErr := saga.Run(context.Background(), &order{ID: "order-1"})

	return errors.Join(runErr, out.failure())
}

// --8<-- [end:run]

func execute(stdout, stderr io.Writer) int {
	err := run(stdout)
	if err == nil {
		return 0
	}

	if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
		return exitWriteFailure
	}

	return exitFailure
}
