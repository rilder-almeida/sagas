package sagas

// Status is the status of a Step. It can be one of the following:
type Status int

const (
	// Undefined indicates that Step status should treat this value as an undefined result. This is the default value.
	Undefined Status = iota
	// Canceled indicates that Step status should treat this value as an interrupt that must not be executed,
	// rollforwarded or rollbackwarded.
	Canceled
	// Failed indicates that Step status should treat this value as a severe failure that did not or cannot be
	// rollforwarded or rollbackwarded.
	Failed
	// Successed indicates the Step status should treat this value as a success. This is the value that will be
	// returned if the Step action succeeds.
	Successed
	// Retry indicates the Retriable should treat this value as a soft failure and retry.
	Retry
)

func (s Status) string() string {
	switch s {
	case Undefined:
		return "Undefined"
	case Canceled:
		return "Canceled"
	case Failed:
		return "Failed"
	case Successed:
		return "Successed"
	case Retry:
		return "Retry"
	default:
		return "invalid status"
	}
}
