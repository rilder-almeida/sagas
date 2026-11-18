package sagas

import "fmt"

// PanicError is the error a panic in a call becomes. It holds the panic value and the stack.
type PanicError struct {
	Value any
	Stack []byte
}

// Error returns the panic value in a message.
func (e *PanicError) Error() string {
	return "sagas: panic: " + fmt.Sprint(e.Value)
}
