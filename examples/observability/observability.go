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

type (
	stockID  string
	chargeID string
)

type order struct {
	ID       string `sagas:"id"`
	StockID  stockID
	ChargeID chargeID
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

type reserve struct{ sagas.Step[order, stockID] }

func (reserve) Execute(ctx context.Context, _ order) (stockID, error) {
	return stockID(sagas.IdempotencyKey(ctx)), ctx.Err()
}

type charge struct {
	sagas.Step[order, chargeID]

	provider *provider
}

func (c *charge) Execute(ctx context.Context, _ order) (chargeID, error) {
	key := sagas.IdempotencyKey(ctx)
	if err := c.provider.charge(key); err != nil {
		return "", err
	}

	return chargeID(key), ctx.Err()
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

// --8<-- [start:requestlog]

type requestKey struct{}

func withRequest(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestKey{}, id)
}

type requestLog struct {
	logger *slog.Logger
}

func (r requestLog) Wrap(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
	id, ok := ctx.Value(requestKey{}).(string)
	if !ok {
		id = "none"
	}

	return sagas.Slog{Logger: r.logger.With("request_id", id)}.Wrap(ctx, info, call)
}

// --8<-- [end:requestlog]

// --8<-- [start:saga]

const (
	attempts   = 2
	retryDelay = time.Millisecond
)

func newSaga(logger *slog.Logger) (*sagas.Saga[order], error) {
	retry := sagas.RetryPolicy{Attempts: attempts, Initial: retryDelay, NoJitter: true}
	flow := sagas.Sequence(&reserve{}, &charge{RetryPolicy: retry, provider: newProvider()})

	return sagas.New[order]("order", flow, sagas.WithMiddleware(requestLog{logger: logger}))
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

	runErr := saga.Run(withRequest(context.Background(), "req-7"), &order{ID: "order-1"})

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
