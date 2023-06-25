package sagas

// Event is an interface that represents a state or status Event.
// It is used to define the type of the Event in the notification struct and
// can be a State or Status.
type Event interface {
	String() string
}

// Status is the status of a Step. It can be one of the following:
// Undefined, Canceled, Failed, Successed, Retry.
type Status int

const (
	// Undefined indicates that Step status should treat this value as an undefined result. This is the default value
	// and indicates that the Step action has not yet initiated.
	Undefined Status = iota
	// Failed indicates that Step status should treat this value as a failure. This is the value that will be
	// returned if the Step action fails even after all retries.
	Failed
	// Successed indicates the Step status should treat this value as a success. This is the value that will be
	// returned if the Step action succeeds before the maximum number of retries is reached.
	Successed
	// retry indicates the retrier should treat this value as a soft failure and retry. This is a internal value
	// and should not be used by the user.
	retry
)

// String returns the string representation of the status.
func (s Status) String() string {
	switch s {
	case Undefined:
		return "Undefined"
	case Failed:
		return "Failed"
	case Successed:
		return "Successed"
	case retry:
		return "Retry"
	default:
		return "invalid status"
	}
}

// State is the state of a step. It can be one of the following:
// Idle, Running, Completed.
type State int

const (
	// Idle indicates that step state should treat this value as a static state. This is the default value
	// and indicates that the Step action has not yet initiated.
	Idle State = iota
	// Running indicates that step state should treat this value as a state that is being executed. This is the value
	// that will be returned if the Step action is running or retrying at the moment.
	Running
	// Completed indicates that step state should treat this value as a state that has been executed. This is the value
	// that will be returned if the Step action has been executed.
	Completed
)

// String returns the string representation of the state.
func (s State) String() string {
	switch s {
	case Idle:
		return "Idle"
	case Running:
		return "Running"
	case Completed:
		return "Completed"
	}
	return "invalid state"
}
