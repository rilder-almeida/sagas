package main

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"syscall"
	"testing"

	"github.com/rilder-almeida/sagas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sink struct {
	writer  io.Writer
	written func() string
}

type writerFactory func(t *testing.T) sink

func bufferWriter(t *testing.T) sink {
	t.Helper()

	var buf bytes.Buffer

	return sink{writer: &buf, written: buf.String}
}

func failingWriter(t *testing.T) sink {
	t.Helper()

	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, root.Close()) })

	created, err := root.Create("out")
	require.NoError(t, err)
	require.NoError(t, created.Close())

	f, err := root.Open("out")
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, f.Close()) })

	return sink{writer: f, written: func() string {
		content, readErr := io.ReadAll(f)
		require.NoError(t, readErr)

		return string(content)
	}}
}

func Test_run(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		newWriter  writerFactory
		wantOutput string
		wantErr    error
	}{
		{
			name:      "[Success] When: the writer accepts all writes ; Should: print the three runs",
			newWriter: bufferWriter,
			wantOutput: "acct-1: completed, referral credited\n" +
				"acct-2: completed, referral skipped\n" +
				"acct-3: compensated, pending: [], undone: [provision register]\n",
		},
		{
			name:      "[Fail] When: the writer fails ; Should: return the write error and write nothing",
			newWriter: failingWriter,
			wantErr:   syscall.EBADF,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out := tt.newWriter(t)

			err := run(out.writer)

			if tt.wantErr != nil {
				var pathErr *fs.PathError

				require.ErrorAs(t, err, &pathErr)
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantOutput, out.written())
		})
	}
}

func Test_execute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		newStdout  writerFactory
		newStderr  writerFactory
		wantCode   int
		wantStderr bool
	}{
		{
			name:      "[Success] When: both writers accept writes ; Should: return 0 and write nothing to stderr",
			newStdout: bufferWriter,
			newStderr: bufferWriter,
		},
		{
			name:       "[Fail] When: stdout fails and stderr works ; Should: return 1 and report on stderr",
			newStdout:  failingWriter,
			newStderr:  bufferWriter,
			wantCode:   1,
			wantStderr: true,
		},
		{
			name:      "[Fail] When: stdout and stderr fail ; Should: return 2 and report nothing",
			newStdout: failingWriter,
			newStderr: failingWriter,
			wantCode:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stdout, stderr := tt.newStdout(t), tt.newStderr(t)

			assert.Equal(t, tt.wantCode, execute(stdout.writer, stderr.writer))
			assert.Equal(t, tt.wantStderr, stderr.written() != "")
		})
	}
}

func Test_newSaga(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		referral     string
		plan         string
		wantReport   string
		wantCredited bool
		wantUndone   []string
	}{
		{
			name:         "[Success] When: the account has a code ; Should: credit the referral and undo nothing",
			referral:     "FRIEND",
			plan:         "basic",
			wantReport:   "acct-9: completed, referral credited\n",
			wantCredited: true,
		},
		{
			name:       "[Success] When: the account has no code ; Should: skip the referral and never compensate it",
			plan:       "basic",
			wantReport: "acct-9: completed, referral skipped\n",
		},
		{
			name:       "[Fail] When: no code and provision fails ; Should: not compensate the skipped referral",
			plan:       "dedicated",
			wantReport: "acct-9: compensated, pending: [], undone: [provision register]\n",
			wantUndone: []string{"provision", "register"},
		},
		{
			name:       "[Fail] When: a code and provision fails ; Should: compensate the referral too",
			referral:   "FRIEND",
			plan:       "dedicated",
			wantReport: "acct-9: compensated, pending: [], undone: [provision referral register]\n",
			wantUndone: []string{"provision", "referral", "register"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newServices()

			saga, err := newSaga(svc)
			require.NoError(t, err)

			a := account{ID: "acct-9", Email: "cai@example.com", Plan: tt.plan, Referral: tt.referral}
			runErr := saga.Run(t.Context(), &a)
			ran := svc.undone.take()

			var out bytes.Buffer

			require.NoError(t, report(&out, &a, ran, runErr))
			assert.Equal(t, tt.wantReport, out.String())

			key := sagas.CallInfo{Execution: a.ID, Step: "referral"}.IdempotencyKey()
			id, putErr := svc.referrals.put(t.Context(), key, "FRIEND")

			require.NoError(t, putErr)
			assert.Equal(t, tt.wantCredited, id == a.ReferralID)
			assert.Equal(t, tt.wantUndone, ran)
		})
	}
}

const (
	stepRegister  = "register"
	stepProvision = "provision"
	stepReferral  = "referral"
)

func compensationOf(step string, svc *services) func(context.Context, *account) error {
	switch step {
	case stepRegister:
		return (&register{accounts: svc.accounts, undone: svc.undone}).Compensate
	case stepProvision:
		return (&provision{servers: svc.servers, undone: svc.undone}).Compensate
	default:
		return (&referral{referrals: svc.referrals, undone: svc.undone}).Compensate
	}
}

func Test_undone_release(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		step       string
		canceled   bool
		wantUndone []string
		wantErr    error
	}{
		{
			name:       "[Success] When: the register removal succeeds ; Should: record register",
			step:       stepRegister,
			wantUndone: []string{stepRegister},
		},
		{
			name:     "[Fail] When: the register removal fails ; Should: return the error and record nothing",
			step:     stepRegister,
			canceled: true,
			wantErr:  context.Canceled,
		},
		{
			name:       "[Success] When: the provision removal succeeds ; Should: record provision",
			step:       stepProvision,
			wantUndone: []string{stepProvision},
		},
		{
			name:     "[Fail] When: the provision removal fails ; Should: return the error and record nothing",
			step:     stepProvision,
			canceled: true,
			wantErr:  context.Canceled,
		},
		{
			name:       "[Success] When: the referral removal succeeds ; Should: record referral",
			step:       stepReferral,
			wantUndone: []string{stepReferral},
		},
		{
			name:     "[Fail] When: the referral removal fails ; Should: return the error and record nothing",
			step:     stepReferral,
			canceled: true,
			wantErr:  context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newServices()
			ctx, cancel := context.WithCancel(t.Context())

			if tt.canceled {
				cancel()
			}

			defer cancel()

			err := compensationOf(tt.step, svc)(ctx, &account{})

			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantUndone, svc.undone.take())
		})
	}
}
