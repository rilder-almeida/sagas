package sagas

import (
	"strconv"
	"strings"
)

// Mermaid returns the saga as a Mermaid flowchart.
func (s *Saga[S]) Mermaid() string {
	lines := []string{"flowchart TD"}

	nodeID := func(i int) string { return "s" + strconv.Itoa(i) }

	var forwardOnly []string

	for i := range s.reports {
		r := &s.reports[i]
		id := nodeID(i)

		switch {
		case r.Pivot:
			lines = append(lines, "    "+id+"{{\""+r.Name+"\"}}")
		case r.Enabled:
			lines = append(lines, "    "+id+"[\""+r.Name+" (if Enabled)\"]")
		default:
			lines = append(lines, "    "+id+"[\""+r.Name+"\"]")
		}

		if s.steps[i].zone == ZoneForwardOnly {
			forwardOnly = append(forwardOnly, id)
		}
	}

	for i := range s.steps {
		for _, p := range s.steps[i].parents {
			lines = append(lines, "    "+nodeID(p)+" --> "+nodeID(i))
		}
	}

	if len(forwardOnly) > 0 {
		lines = append(lines, "    classDef forwardOnly stroke-dasharray: 5 5",
			"    class "+strings.Join(forwardOnly, ",")+" forwardOnly")
	}

	return strings.Join(lines, "\n") + "\n"
}
