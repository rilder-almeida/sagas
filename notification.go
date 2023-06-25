package sagas

import "errors"

// Notification is a struct that represents a Notification.
type Notification struct {
	// Identifier is a string that represents the Identifier of step that emitted
	// the notification.
	Identifier Identifier
	// Event is an interface that represents a state or status Event emitted by
	// the step.
	Event Event
}

// NewNotification is a function that creates a new notification struct.
// It receives an identifier and an event as parameters and returns a notification
// struct and an error. If the identifier is empty or the event is invalid, it
// returns an error. And if the event is a State or Status, it returns an error.
func NewNotification(id Identifier, event Event) (Notification, error) {
	if id.String() == "" {
		return Notification{}, errors.New("invalid identifier")
	}

	if err := validateEvent(event); err != nil {
		return Notification{}, err
	}

	return Notification{
		Identifier: id,
		Event:      event,
	}, nil
}

func validateEvent(event Event) error {
	if !isState(event) && !isStatus(event) || event == nil {
		return errors.New("invalid event")
	}
	return nil
}

func isState(event Event) bool {
	_, ok := event.(State)
	return ok
}

func isStatus(event Event) bool {
	_, ok := event.(Status)
	return ok
}
