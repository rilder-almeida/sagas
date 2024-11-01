# sagas

This package provides a framework composed by the following objects: Saga, Step and Action. Action abstracts a function that will be executed by a Step. The Step executes the Action and publishes a event for the Saga, using the Notifier. The Saga cares about the execution of the steps, it receives the events from the Notifier through the Observer. The Saga's observer holds the Execution Plan that will determine the next Step to be executed.

The following diagram shows the relationship between the objects:

	saga starts ->- run step ->- execute action ->- publish event ->- notify saga ->- saga observes ->- execution plan ->- next step...

## Overview

The sagas package is a framework to implement the saga pattern in Go. It was inspired on the article "SAGAS" by Hector Garcia-Molina, Kenneth Salem, and Harold F. Korth, published by the ACM in 1987, and the article "Implementing Sagas" by Caitie McCaffrey, published by Microsoft in 2016.

Initially, the saga pattern was proposed to solve the problem of long-lived and complex transactions in distributed systems. In most cases, LLTs must preserve the consistency in the data, atomicity of the transactions and have to be a fault-tolerant process. Besides that, TTLs use to access many objects that may cause a deadlock or locks in the database for a long time.

In a short definition, a saga breaks a long-lived transaction into a sequence of transactions that can be executed in a distributed environment. Each transaction process data and publishes a message or event to trigger the next transactionin in the saga. If a transaction fails then a compensation must be executed to undo the changes or in some cases to make a compensating change. In other words, compensation transactions can be a rollback or roll-forward transaction. The transactions and compensations together form a saga.

For more information about the saga pattern, please read the articles:
 - [SAGAS](https://www.cs.cornell.edu/andru/cs711/2002fa/reading/sagas.pdf) by Hector Garcia-Molina, Kenneth Salem, and Harold F. Korth (1987)
 - [Applying the Saga Pattern](https://www.youtube.com/watch?v=xDuwrtwYHu8) by Caitie McCaffrey (2015)
 - [Distributed Sagas](https://github.com/aphyr/dist-sagas/blob/master/sagas.pdf) by Caitie McCaffrey, Kyle Kingsbury and Neha Narula (2016)
 - [Distributed Sagas: A Protocol for Coordinating Microservices](https://www.youtube.com/watch?v=0UTOLRTwOX0) by Caitie McCaffrey (2017)
 - [Fault-Tolerance and Data Consistency Using Distributed Sagas](https://sookocheff.com/post/architecture/fault-tolerance-and-data-consistency-using-distributed-sagas/) by Kevin Sookocheff (2018)

## Implementation

```go
func main() {
	// Given a number to divide
	dividend := flag.Int("number", 0, "an int to divide")
	flag.Parse()

	// Create a Retrier to retry the action if it fails
	retrier := sagas.NewRetrier(sagas.BackoffConstant(3, 1*time.Second), nil)

	// Create a action function to verify if the number is divisible by 2
	actionFnVerify := func(number int) func(context.Context) error {
		return func(ctx context.Context) error {
			if number%2 != 0 {
				log.Println("number is not divisible by 2: ", number)
				return errors.New("number is not divisible by 2")
			}
			log.Println("number is divisible by 2: ", number)
			return nil
		}
	}

	// Create a step to verify if the number is divisible by 2
	stepVerify := sagas.NewStep("verify", actionFnVerify(dividend), retrier)

	// Create a action function to divide the number by 2
	actionFnDivide := func(number int) func(context.Context) error {
		return func(ctx context.Context) error {
			number = number / 2
			log.Println("number divided by 2: ", number)
			return nil
		}
	}

	// Create a action function to finish the saga
	actionFnFinish := func() func(context.Context) error {
		return func(ctx context.Context) error {
			log.Println("saga finished")
			return nil
		}
	}

	// Create a step to finish the saga
	stepFinish := sagas.NewStep("finish", actionFnFinish(), nil)

	// Create a step to divide the number by 2
	stepDivide := sagas.NewStep("divide", actionFnDivide(dividend), retrier)

	// Create a saga
	saga := sagas.NewSaga()

	// Add the steps to the saga
	saga.AddSteps(stepVerify, stepDivide, stepFinish)

	// Plan the execution of the steps
	saga.When(stepVerify).Is(sagas.Failed).Then(sagas.NewAction(stepFinish.Run)).Plan()
	saga.When(stepVerify).Is(sagas.Successed).Then(sagas.NewAction(stepDivide.Run)).Plan()

	saga.When(stepDivide).Is(sagas.Completed).Then(sagas.NewAction(stepFinish.Run)).Plan()

	// Execute the saga
	saga.Run(context.Background(), func() bool {
		return stepFinish.GetState() == sagas.Completed
	})
}
```
