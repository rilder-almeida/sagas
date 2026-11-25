package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"slices"
	"sync"

	"github.com/rilder-almeida/sagas"
)

const (
	exitFailure      = 1
	exitWriteFailure = 2
)

// --8<-- [start:state]

type (
	seat     string
	chargeID string
	receipt  string
)

type booking struct {
	ID       string `sagas:"id"`
	Seat     seat
	ChargeID chargeID
	Receipt  receipt
}

// --8<-- [end:state]

// --8<-- [start:steps]

type reserve struct{ sagas.Step[booking, seat] }

func (reserve) Execute(ctx context.Context, _ booking) (seat, error) { return "12A", ctx.Err() }

type charge struct{ sagas.Pivot[booking, chargeID] }

func (charge) Execute(ctx context.Context, _ booking) (chargeID, error) {
	return chargeID(sagas.IdempotencyKey(ctx)), ctx.Err()
}

type notify struct{ sagas.Step[booking, receipt] }

func (notify) Execute(ctx context.Context, _ booking) (receipt, error) { return "sent", ctx.Err() }

// --8<-- [end:steps]

// --8<-- [start:journal]

type fileJournal struct {
	mu   sync.Mutex
	root *os.Root
}

func openFileJournal(dir string) (*fileJournal, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}

	return &fileJournal{root: root}, nil
}

func (j *fileJournal) Close() error { return j.root.Close() }

const fileExtension = ".jsonl"

func journalFile(execution string) string { return url.PathEscape(execution) + fileExtension }

// --8<-- [end:journal]

// --8<-- [start:append]

func (j *fileJournal) Append(ctx context.Context, e *sagas.Event) error {
	if err := sagas.CheckEvent(e); err != nil {
		return err
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	stored, err := readEvents(j.root, e.Execution)
	if err != nil {
		return err
	}

	switch {
	case e.Seq == 1 && len(stored) == 0:
		return createEvent(j.root, e)
	case e.Seq == len(stored)+1:
		return writeEvent(j.root, e)
	case e.Seq <= len(stored) && sagas.SameEvent(&stored[e.Seq-1], e):
		return syncDir(j.root)
	case e.Seq == 1:
		return fmt.Errorf("%w: execution %q", sagas.ErrExecutionExists, e.Execution)
	default:
		return fmt.Errorf("%w: execution %q seq %d", sagas.ErrConcurrentWriter, e.Execution, e.Seq)
	}
}

func createEvent(root *os.Root, e *sagas.Event) error {
	if err := writeEvent(root, e); err != nil {
		return err
	}

	return syncDir(root)
}

const fileMode = 0o600

type eventLine struct {
	*sagas.Event

	Values []valueLine
}

type valueLine struct {
	Field string
	Value jsontext.Value `json:",omitzero"`
}

func lineOf(e *sagas.Event) eventLine {
	line := eventLine{Event: e}
	for _, v := range e.Values {
		line.Values = append(line.Values, valueLine{Field: v.Field, Value: v.Value})
	}

	return line
}

func (l *eventLine) event() sagas.Event {
	e := *l.Event
	e.Values = nil

	for _, v := range l.Values {
		e.Values = append(e.Values, sagas.FieldValue{Field: v.Field, Value: v.Value})
	}

	return e
}

func writeEvent(root *os.Root, e *sagas.Event) error {
	line, err := json.Marshal(lineOf(e))
	if err != nil {
		return err
	}

	f, err := root.OpenFile(journalFile(e.Execution), os.O_APPEND|os.O_CREATE|os.O_WRONLY, fileMode)
	if err != nil {
		return err
	}

	return errors.Join(appendLine(f, append(line, '\n')), f.Close())
}

func appendLine(f *os.File, line []byte) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}

	if _, err = f.Write(line); err == nil {
		err = f.Sync()
	}

	if err != nil {
		return errors.Join(err, f.Truncate(info.Size()))
	}

	return nil
}

func syncDir(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}

	return errors.Join(dir.Sync(), dir.Close())
}

// --8<-- [end:append]

// --8<-- [start:events]

func (j *fileJournal) Events(ctx context.Context, execution string) ([]sagas.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	events, err := readEvents(j.root, execution)
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return nil, fmt.Errorf("%w: execution %q", sagas.ErrNoEvents, execution)
	}

	return events, nil
}

var (
	errCutLine = errors.New("last line is cut")
	errBadLine = errors.New("line is not an event")
)

func readEvents(root *os.Root, execution string) ([]sagas.Event, error) {
	data, err := root.ReadFile(journalFile(execution))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	if len(data) > 0 && data[len(data)-1] != '\n' {
		return nil, fmt.Errorf("%w: execution %q", errCutLine, execution)
	}

	var events []sagas.Event

	for line := range bytes.Lines(data) {
		stored := eventLine{Event: new(sagas.Event)}
		if err = json.Unmarshal(line, &stored); err != nil {
			return nil, fmt.Errorf("%w: execution %q line %d: %w", errBadLine, execution, len(events)+1, err)
		}

		e := stored.event()

		if err = sagas.CheckEvent(&e); err != nil {
			return nil, fmt.Errorf("execution %q line %d: %w", execution, len(events)+1, err)
		}

		if e.Execution != execution || e.Seq != len(events)+1 {
			return nil, fmt.Errorf("%w: execution %q line %d holds execution %q seq %d",
				errBadLine, execution, len(events)+1, e.Execution, e.Seq)
		}

		events = append(events, e)
	}

	return events, nil
}

// --8<-- [end:events]

// --8<-- [start:saga]

func newSaga(journal sagas.Journal) (*sagas.Saga[booking], error) {
	return sagas.New[booking]("booking", sagas.Sequence(&reserve{}, &charge{}, &notify{}), sagas.WithJournal(journal))
}

// --8<-- [end:saga]

func run(w io.Writer) error {
	dir, err := os.MkdirTemp("", "filejournal")
	if err != nil {
		return err
	}

	return errors.Join(runIn(w, dir), os.RemoveAll(dir))
}

// --8<-- [start:run]

func runIn(w io.Writer, dir string) error {
	journal, err := openFileJournal(dir)
	if err != nil {
		return err
	}

	return errors.Join(runWith(w, journal), journal.Close())
}

func runWith(w io.Writer, journal *fileJournal) error {
	saga, err := newSaga(journal)
	if err != nil {
		return err
	}

	ctx := context.Background()
	b := booking{ID: "booking-1"}

	if runErr := saga.Run(ctx, &b); runErr != nil {
		return runErr
	}

	events, err := journal.Events(ctx, b.ID)
	if err != nil {
		return err
	}

	return report(w, events)
}

func report(w io.Writer, events []sagas.Event) error {
	counts := make(map[sagas.EventKind]int)

	for i := range events {
		counts[events[i].Kind]++
	}

	for _, kind := range slices.Sorted(maps.Keys(counts)) {
		if _, err := fmt.Fprintf(w, "%s: %d\n", kind, counts[kind]); err != nil {
			return err
		}
	}

	return nil
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
