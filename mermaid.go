package sagas

import (
	"fmt"
	"strconv"
	"strings"
)

// Mermaid returns the saga as a Mermaid flowchart. Node ids are s plus the index of the step in the flow, the
// pivot is a hexagon, a step with Enabled ends in "(if)", and forward-only steps join the forwardOnly class.
func (s *Saga[S]) Mermaid() string {
	var b strings.Builder

	b.WriteString("flowchart TD\n")

	var forwardOnly []string

	for i := range s.reports {
		r := &s.reports[i]
		label := r.Name

		switch {
		case r.Pivot:
			fmt.Fprintf(&b, "    s%d{{\"%s\"}}\n", i, label)
		case r.Enabled:
			fmt.Fprintf(&b, "    s%d[\"%s (if)\"]\n", i, label)
		default:
			fmt.Fprintf(&b, "    s%d[\"%s\"]\n", i, label)
		}

		if s.steps[i].zone == ZoneForwardOnly {
			forwardOnly = append(forwardOnly, "s"+strconv.Itoa(i))
		}
	}

	for i := range s.steps {
		for _, p := range s.steps[i].parents {
			fmt.Fprintf(&b, "    s%d --> s%d\n", p, i)
		}
	}

	if len(forwardOnly) > 0 {
		b.WriteString("    classDef forwardOnly stroke-dasharray: 5 5\n")
		fmt.Fprintf(&b, "    class %s forwardOnly\n", strings.Join(forwardOnly, ","))
	}

	return b.String()
}
