package router

// Task status constants mirror the Python Hub state machine (M1).
const (
	StatusPending    = "pending"
	StatusRunning    = "running"
	StatusCancelling = "cancelling"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusLost       = "lost"
	StatusCancelled  = "cancelled"
)

var (
	validStatuses = map[string]struct{}{
		StatusPending: {}, StatusRunning: {}, StatusCancelling: {},
		StatusCompleted: {}, StatusFailed: {}, StatusLost: {}, StatusCancelled: {},
	}
	terminalStatuses = map[string]struct{}{
		StatusCompleted: {}, StatusFailed: {}, StatusLost: {}, StatusCancelled: {},
	}
)

// ValidateTransition checks whether a status change is allowed.
func ValidateTransition(from, to string) error {
	if _, ok := validStatuses[from]; !ok {
		return &Error{Msg: "invalid status in transition " + from + " -> " + to}
	}
	if _, ok := validStatuses[to]; !ok {
		return &Error{Msg: "invalid status in transition " + from + " -> " + to}
	}
	allowed := map[string]map[string]struct{}{
		StatusPending: {StatusRunning: {}, StatusCancelled: {}, StatusLost: {}},
		StatusRunning: cloneSet(terminalStatuses),
		StatusCancelling: {
			StatusCancelled: {}, StatusCompleted: {}, StatusFailed: {}, StatusLost: {},
		},
	}
	// Internal path used by cancel/timeout handling in the Python Hub.
	allowed[StatusRunning][StatusCancelling] = struct{}{}

	next, ok := allowed[from]
	if !ok || !contains(next, to) {
		return &Error{Msg: "invalid transition " + from + " -> " + to}
	}
	return nil
}

func cloneSet(in map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for k := range in {
		out[k] = struct{}{}
	}
	return out
}

func contains(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

func IsTerminal(status string) bool {
	_, ok := terminalStatuses[status]
	return ok
}
