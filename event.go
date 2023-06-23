package sagas

import "errors"

// event is an interface that represents a state or status event.
// It is used to define the type of the event in the notification struct and
// can be a State or Status.
type event interface {
	String() string
}

// notification is a struct that represents a notification.
type notification struct {
	// identifier is a string that represents the identifier of step that emitted
	// the notification.
	identifier identifier
	// event is an interface that represents a state or status event emitted by
	// the step.
	event event
}

// NewNotification is a function that creates a new notification struct.
// It receives an identifier and an event as parameters and returns a notification
// struct and an error. If the identifier is empty or the event is invalid, it
// returns an error. And if the event is a State or Status, it returns an error.
func NewNotification(id identifier, event event) (notification, error) {
	if id == "" {
		return notification{}, errors.New("invalid identifier")
	}

	if err := validateEvent(event); err != nil {
		return notification{}, err
	}

	return notification{
		identifier: id,
		event:      event,
	}, nil
}

func validateEvent(event event) error {
	if !isState(event) && !isStatus(event) || event == nil {
		return errors.New("invalid event")
	}
	return nil
}

func isState(event event) bool {
	_, ok := event.(State)
	return ok
}

func isStatus(event event) bool {
	_, ok := event.(Status)
	return ok
}
