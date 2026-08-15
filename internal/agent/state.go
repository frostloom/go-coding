// Package agent implements the single-dispatcher state machine that drives
// the coding loop: planning → editing → executing → validating → repairing.
package agent

// State is the phase of the agent loop.
type State string

const (
	StatePlanning   State = "planning"
	StateEditing    State = "editing"
	StateExecuting  State = "executing"
	StateValidating State = "validating"
	StateRepairing  State = "repairing"
	StateDone       State = "done"
	StateFailed     State = "failed"
)
