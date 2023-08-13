package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"time"

	"github.com/rilder-almeida/sagas"
)

// To run this example, execute the following command:
// go run example/single_saga/main.go -number=10

func main() {
	// Given a number to divide
	dividend := flag.Int("number", 0, "an int to divide")
	flag.Parse()

	// Create a Retrier to retry the action if it fails
	retrier := sagas.NewRetrier(sagas.BackoffConstant(3, 1*time.Second))

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
	stepVerify := sagas.NewStep(
		"verify",
		actionFnVerify(*dividend),
		sagas.WithStepRetrier(retrier),
	)

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
	stepFinish := sagas.NewStep("finish", actionFnFinish())

	// Create a step to divide the number by 2
	stepDivide := sagas.NewStep(
		"divide",
		actionFnDivide(*dividend),
		sagas.WithStepRetrier(retrier),
	)

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
