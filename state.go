package sagas

// State is the state of a step. It can be one of the following:
type State int

const (
	// Idle indicates that step state should treat this value as a static state. This is the default value.
	Idle State = iota
	// Running indicates that step state should treat this value as a state that is being executed.
	Running
	// Completed indicates that step state should treat this value as a state that has been executed.
	Completed
)

func (s State) string() string {
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
