# Middleware recipes and run settings

This page answers seven questions about a running saga. A saga is a business operation split into steps. Each answer is code that you copy and adapt. The code uses only the Go standard library and the `sagas` package. The [glossary](../glossary.md) defines the terms of the library. The [guarantees](../explanation/guarantees.md) page explains why the library works this way.

The examples use these names from your own code:

- `order` is the type of your saga state. The state is the value that a run reads and writes.
- `reserve` and `ship` are two of your step types. A step is a named unit of work. Each type embeds `sagas.Step`. `reserve` also has a compensation: a `Compensate` method that undoes the effect of the step.
- `flow` is the `Flow` that you build with `sagas.Sequence` or `sagas.Parallel`. A flow is the set of steps in their order.

## Before you start: how middleware runs

Middleware is a value with the method `Wrap(ctx context.Context, info sagas.CallInfo, call sagas.Call) error`. A plain function with that signature becomes middleware through `sagas.MiddlewareFunc`. The library calls `Wrap` once for each attempt. An attempt is one call of an action or of a compensation. The action is the method that a step runs forward. `info` describes the call: the saga name, the execution ID, the step name, the phase and the attempt number. An execution is one run of the saga over one state value, and the execution ID names it. The phase tells which step method runs: `sagas.PhaseExecute`, `sagas.PhasePivot` or `sagas.PhaseCompensate`.

Your `Wrap` must obey three rules:

1. Run `call` with `ctx` or with a context made from `ctx`.
2. Return only after every `call` that you started returned.
3. Never run `call` twice at the same time.

If `Wrap` returns while `call` still runs, the attempt ends with `sagas.ErrCallUnfinished`. If `Wrap` starts `call` while an earlier `call` still runs, the second `call` does not run and returns `sagas.ErrCallConcurrent`.

There are two places for middleware:

- To wrap every step of a saga, pass the middleware to `sagas.WithMiddleware` in `sagas.New`.
- To wrap one step, give the step type a method `Wrap` with the same signature.

`Wrap` can return without running `call`. The attempt then ends with the error that `Wrap` returns. The library counts a context error, such as `context.Canceled` or `context.DeadlineExceeded`, as an unknown outcome: nobody knows whether the effect outside happened. It counts any other error as a failure. If `Wrap` returns nil without running `call`, the attempt fails with `sagas.ErrCallNotRun`, and the step makes no more attempts.

## How do I limit how many calls run at the same time?

A semaphore is a counter of free slots. A buffered channel is a semaphore. Each call takes a slot. At its end, the call gives the slot back.

1. Add this type to your code:

   ```go
   type semaphore chan struct{}

   func (s semaphore) Wrap(ctx context.Context, _ sagas.CallInfo, call sagas.Call) error {
   	select {
   	case s <- struct{}{}:
   	case <-ctx.Done():
   		return ctx.Err()
   	}
   	defer func() { <-s }()

   	return call(ctx)
   }
   ```

2. Make one semaphore with the number of slots. This one allows 10 calls at the same time:

   ```go
   limit := make(semaphore, 10)
   ```

3. Pass it to `sagas.New`:

   ```go
   saga, err := sagas.New[order]("order", flow, sagas.WithMiddleware(limit))
   ```

If you pass the same `limit` to several sagas, they share its 10 slots.

The time that a call waits for a slot counts in the `AttemptTimeout` of an action. `AttemptTimeout` is a field of `sagas.Step` that bounds each attempt of an action. If the context ends during the wait, the attempt returns the context error, and the outcome is unknown. The pivot is the step after which the saga only moves forward. If the run then undoes the steps before the pivot, it also compensates this step, although the step never ran. The compensation must accept a step that never applied.

## How do I limit how fast calls start?

A rate limit keeps a fixed interval between the starts of two calls.

1. Add this type to your code:

   ```go
   type rateLimit struct {
   	mu    sync.Mutex
   	every time.Duration
   	next  time.Time
   }

   func (r *rateLimit) Wrap(ctx context.Context, _ sagas.CallInfo, call sagas.Call) error {
   	r.mu.Lock()
   	start := time.Now()
   	if start.Before(r.next) {
   		start = r.next
   	}
   	r.next = start.Add(r.every)
   	r.mu.Unlock()

   	timer := time.NewTimer(time.Until(start))
   	defer timer.Stop()

   	select {
   	case <-timer.C:
   		return call(ctx)
   	case <-ctx.Done():
   		return ctx.Err()
   	}
   }
   ```

2. Pass one value to `sagas.New`. This one starts at most 10 calls each second:

   ```go
   saga, err := sagas.New[order]("order", flow, sagas.WithMiddleware(&rateLimit{every: 100 * time.Millisecond}))
   ```

The wait follows the same rules as the wait of the semaphore above.

## How do I stop calling a service that keeps failing or timing out?

A circuit breaker counts the attempts of a service that end in a failure or an unknown outcome. A timeout and a cancel by the caller are unknown outcomes, so they count too. Each attempt counts, so one run can add several. After a limit, the breaker opens: for a cooldown period, it rejects each call and does not run it. A breaker belongs to one service, so you put it on the steps that call that service. The `Wrap` method of a step is middleware for that one step.

1. Add this type to your code:

   ```go
   var errOpen = errors.New("circuit open")

   type breaker struct {
   	mu       sync.Mutex
   	limit    int
   	cooldown time.Duration
   	failures int
   	openTill time.Time
   }

   func (b *breaker) Wrap(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
   	if info.Phase == sagas.PhaseCompensate {
   		return call(ctx)
   	}

   	b.mu.Lock()
   	open := time.Now().Before(b.openTill)
   	b.mu.Unlock()

   	if open {
   		return errOpen
   	}

   	err := call(ctx)

   	b.mu.Lock()
   	defer b.mu.Unlock()

   	if err == nil {
   		b.failures = 0

   		return nil
   	}

   	b.failures++
   	if b.failures >= b.limit {
   		b.failures = 0
   		b.openTill = time.Now().Add(b.cooldown)
   	}

   	return err
   }
   ```

2. Add a `*breaker` field to the step type that calls the service, and give the type a `Wrap` method:

   ```go
   type ship struct {
   	sagas.Step

   	breaker *breaker
   }

   func (s *ship) Wrap(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
   	return s.breaker.Wrap(ctx, info, call)
   }
   ```

3. Make one breaker for each service, and give it to each step that calls that service. This breaker opens after 5 such attempts in a row and stays open for 30 seconds:

   ```go
   carrier := &breaker{limit: 5, cooldown: 30 * time.Second}
   step := &ship{breaker: carrier}
   ```

The breaker lets every compensation through, because a rejected compensation leaves the undo for a person. A rejected call is a failure, so the retry policy of the step decides whether to try again. The retry policy is the `Retry` field of the step. The [retry section](#how-do-i-use-one-retry-setting-on-several-steps) below shows it.

## How do I stop the retries on an error that cannot heal?

Some errors stay the same on every attempt, such as a declined card. `sagas.Permanent(err)` marks an error so that the retries stop at once. Middleware can add this mark to an error of the call.

1. Add this function to your code. Replace `errDeclined` with the error of your service:

   ```go
   var errDeclined = errors.New("card declined")

   func permanentDeclines(ctx context.Context, _ sagas.CallInfo, call sagas.Call) error {
   	err := call(ctx)
   	if errors.Is(err, errDeclined) {
   		return sagas.Permanent(err)
   	}

   	return err
   }
   ```

2. Pass it to `sagas.New` through `sagas.MiddlewareFunc`:

   ```go
   saga, err := sagas.New[order]("order", flow, sagas.WithMiddleware(sagas.MiddlewareFunc(permanentDeclines)))
   ```

This recipe only swaps one error for another, and `errors.Is` still finds `errDeclined`. Middleware cannot turn an error into a success. If `Wrap` returns nil after the call returned an error, the attempt ends with the error of the call. Some answers of a service mean success, such as "already booked" on a repeated request. Put that rule in the action: the action reads the answer and returns nil.

## How do I give each compensation attempt a deadline?

The `AttemptTimeout` of a step bounds each attempt of the action, but not of the compensation. Middleware can set a deadline on each compensation attempt. It reads the phase from `info.Phase`.

1. Add this function to your code:

   ```go
   func compensationDeadline(d time.Duration) sagas.MiddlewareFunc {
   	return func(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
   		if info.Phase != sagas.PhaseCompensate {
   			return call(ctx)
   		}

   		ctx, cancel := context.WithTimeout(ctx, d)
   		defer cancel()

   		return call(ctx)
   	}
   }
   ```

2. Pass it to `sagas.New`. This one gives each compensation attempt 10 seconds:

   ```go
   saga, err := sagas.New[order]("order", flow, sagas.WithMiddleware(compensationDeadline(10*time.Second)))
   ```

The compensation must honor its context. An attempt that passes the deadline returns `context.DeadlineExceeded`, and the retry policy of the step decides whether to try again.

## How do I use one retry setting on several steps?

A retry policy sets how many attempts a call gets and how long to wait between them. The `Retry` field of `sagas.Step` holds it. The policy of a step governs its compensation too. A step with `sagas.NoRetry()` makes one attempt of its action and one attempt of its compensation.

1. Make one `sagas.RetryPolicy` value. This one allows 5 attempts and keeps the default delays:

   ```go
   retry5 := sagas.RetryPolicy{Attempts: 5}
   ```

2. Set it as the `Retry` field of each step:

   ```go
   hold := &reserve{}
   hold.Retry = retry5

   send := &ship{breaker: carrier}
   send.Retry = retry5
   ```

If many steps share more than one setting, you can write a function that builds the `sagas.Step` value instead of steps 1 and 2:

```go
func reliable(name string) sagas.Step {
	return sagas.Step{Name: name, Retry: sagas.RetryPolicy{Attempts: 5}}
}
```

Then use the function for each step:

```go
hold := &reserve{Step: reliable("reserve")}
```

A compensation can still fail after its last attempt. The `CompensationFailure` field of the step then decides what happens to the other compensations. Each policy ends the run with `sagas.ErrCompensationFailed`.

- `sagas.CompensationBlock`, the default, stops each compensation that waits on this one. A branch is one path of a `sagas.Parallel` flow. The branches that do not wait on this compensation go on.
- `sagas.CompensationHalt` stops every compensation that remains.
- `sagas.CompensationContinue` stops nothing.

With each policy, the failed compensation stays undone, and a person must finish that undo by hand. With `CompensationBlock` and `CompensationHalt`, the person also runs the compensations that the policy stopped. To set the halt policy on a step, write:

```go
hold.CompensationFailure = sagas.CompensationHalt
```

To find the steps that need the person, read the `Pending` field of the error of `Run`:

```go
err := saga.Run(ctx, &o)

var runErr *sagas.RunError
if errors.As(err, &runErr) && errors.Is(err, sagas.ErrCompensationFailed) {
	slog.Error("undo by hand", "pending", runErr.Pending)
}
```

## How do I set a time limit on a run?

`Run` has no clock of its own while the context of the caller is alive. To bound a run, set a deadline on that context.

When your context ends, the library keeps working for a grace period, five minutes by default. It goes on with the compensations, a pivot that started and the steps after the pivot. It starts no new step before the pivot and no new pivot.

1. Make a context with a deadline, and pass it to `Run`:

   ```go
   ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
   defer cancel()

   err := saga.Run(ctx, &o)
   ```

2. If you want a grace period other than five minutes, pass `sagas.WithRecoveryTimeout` to `sagas.New`:

   ```go
   saga, err := sagas.New[order]("order", flow, sagas.WithRecoveryTimeout(time.Minute))
   ```

3. If `Run` returns an error, test your own context or `sagas.ErrGraceEnded` to learn whether the deadline passed:

   ```go
   if err != nil && (ctx.Err() != nil || errors.Is(err, sagas.ErrGraceEnded)) {
   	slog.Warn("the run ended after the deadline", "err", err)
   }
   ```

   A nil error means that the run completed: each step succeeded or the library skipped it, and the journal recorded the end. The journal is the record of what an execution intends and did. A deadline that passed after the last step started does not change that.

The grace period starts at the end of your context, never earlier. At the end of the grace period, the library stops the work that goes on and marks the error with `sagas.ErrGraceEnded`. If every step, every compensation and the journal honor their context, a run takes at most the deadline plus the grace period. The [grace period section](../explanation/guarantees.md#the-grace-period) of the guarantees page explains why.

Do not test the error of a step for `context.Canceled` to learn whether you canceled. A step can return that error for its own reasons.
