package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/rilder-almeida/sagas"
)

const (
	exitFailure      = 1
	exitWriteFailure = 2

	carrierOpen = "open"
)

// --8<-- [start:state]

type (
	stockID    string
	chargeID   string
	trackingID string
	mailID     string
)

type order struct {
	ID         string `sagas:"id"`
	Carrier    string
	StockID    stockID
	ChargeID   chargeID
	TrackingID trackingID
	MailID     mailID
}

// --8<-- [end:state]

// --8<-- [start:steps]

type reserve struct{ sagas.Step[order, stockID] }

func (reserve) Execute(ctx context.Context, _ order) (stockID, error) {
	return stockID(sagas.IdempotencyKey(ctx)), ctx.Err()
}

type charge struct{ sagas.Pivot[order, chargeID] }

func (charge) Execute(ctx context.Context, _ order) (chargeID, error) {
	return chargeID(sagas.IdempotencyKey(ctx)), ctx.Err()
}

const carrierClosed = "closed"

var errCarrierClosed = errors.New("carrier is closed")

type ship struct{ sagas.Step[order, trackingID] }

func (ship) Execute(ctx context.Context, o order) (trackingID, error) {
	if o.Carrier == carrierClosed {
		return "", sagas.Permanent(errCarrierClosed)
	}

	return trackingID(sagas.IdempotencyKey(ctx)), ctx.Err()
}

type notify struct{ sagas.Step[order, mailID] }

func (notify) Execute(ctx context.Context, _ order) (mailID, error) {
	return mailID(sagas.IdempotencyKey(ctx)), ctx.Err()
}

// --8<-- [end:steps]

// --8<-- [start:saga]

func newSaga(journal sagas.Journal) (*sagas.Saga[order], error) {
	flow := sagas.Sequence(&reserve{}, &charge{}, &ship{}, &notify{})

	return sagas.New[order]("order", flow, sagas.WithJournal(journal))
}

// --8<-- [end:saga]

// --8<-- [start:run]

func run(w io.Writer) error {
	journal := sagas.NewMemoryJournal()

	saga, err := newSaga(journal)
	if err != nil {
		return err
	}

	ctx := context.Background()
	o := order{ID: "order-1", Carrier: carrierClosed}

	return inspectRun(ctx, w, journal, o.ID, saga.Run(ctx, &o))
}

// --8<-- [end:run]

// --8<-- [start:inspect-run]

func inspectRun(ctx context.Context, w io.Writer, journal sagas.EventReader, execution string, runErr error) error {
	var failed *sagas.RunError

	switch {
	case runErr == nil:
		_, err := fmt.Fprintf(w, "%s: completed\n", execution)

		return err
	case !errors.As(runErr, &failed):
		return runErr
	}

	events, err := currentEvents(ctx, journal, execution)
	if err != nil {
		return err
	}

	if _, err = fmt.Fprintf(w, "%s: %s, pending: %v\n", execution, failed.Kind.Name(), failed.Pending); err != nil {
		return err
	}

	for _, step := range failed.Pending {
		if _, err = fmt.Fprintf(w, "%s: %s\n", step, lastEventOf(events, step)); err != nil {
			return err
		}
	}

	return nil
}

func currentEvents(ctx context.Context, journal sagas.EventReader, execution string) ([]sagas.Event, error) {
	events, err := journal.Events(ctx, execution)
	if err != nil {
		return nil, err
	}

	if versionErr := checkVersions(execution, events); versionErr != nil {
		return nil, versionErr
	}

	return events, nil
}

var errUnknownVersion = errors.New("unknown event version")

func checkVersions(execution string, events []sagas.Event) error {
	for i := range events {
		if events[i].Version != sagas.EventVersion {
			return fmt.Errorf("%w: execution %q event %d has version %q",
				errUnknownVersion, execution, i+1, events[i].Version)
		}
	}

	return nil
}

func lastEventOf(events []sagas.Event, step string) string {
	for i := range slices.Backward(events) {
		if events[i].Step == step {
			return fmt.Sprintf("last event %s, error: %s", events[i].Kind, cmp.Or(events[i].Err.Message, "none"))
		}
	}

	return "no event"
}

// --8<-- [end:inspect-run]

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
