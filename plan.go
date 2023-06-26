package sagas

// planMap is a map that contains a list of actions to be executed when an event
// occurs for determinated identifier.
type planMap map[Identifier]map[Event][]Action

// plan is an interface that represents a plan. It is responsible for storing
// the actions to be executed when an event occurs for determinated identifier.
type plan interface {
	// add adds an Action to the plan.
	add(Identifier, Event, ...Action)
	// get returns a list of actions and a boolean.
	get(Identifier, Event) ([]Action, bool)
}

// newPlan returns a new plan. It is responsible for initializing the plan
// struct.
func newPlan() plan {
	return make(planMap)
}

// add adds an Action to the plan. It receives an identifier, an event and a
// list of actions as parameters and stores it in the plan. If the identifier
// or event does not exist in the plan, it creates a new entry.
func (p planMap) add(identifier Identifier, event Event, actions ...Action) {
	if _, ok := p[identifier]; !ok {
		p[identifier] = make(map[Event][]Action)
	}

	if _, ok := p[identifier][event]; !ok {
		p[identifier][event] = make([]Action, 0)
	}

	p[identifier][event] = append(p[identifier][event], actions...)
}

// get returns a list of actions and a boolean. It receives an identifier and
// an event as parameters and returns a list of actions and a boolean. If the
// identifier or event does not exist in the plan, it returns false.
func (p planMap) get(identifier Identifier, event Event) ([]Action, bool) {
	if _, ok := p[identifier]; !ok {
		return nil, false
	}

	if _, ok := p[identifier][event]; !ok {
		return nil, false
	}

	return p[identifier][event], true
}
