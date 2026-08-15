package agent

import "context"

// HookPoint identifies a lifecycle moment inside the agent loop where a
// governance/observability hook can run.
//
//	before_edit  — before each file edit is applied (veto-capable)
//	after_edit   — after an edit was applied (observable)
//	before_test  — before the test run (veto-capable)
//	after_test   — after the test run (observable)
//	before_chat  — before each producer LLM call (veto-capable)
//	on_error     — when the task transitions to the failed state (observable)
//
// A vetoing hook (returns non-nil error) blocks the action and routes the
// agent into REPAIRING with the hook's reason fed back as memory — the same
// repair-before-test loop that edit validation uses. After-hooks are
// observability: their errors are logged as events, never fatal.
//
// before_chat is special: it is the ONE veto point that can stop the agent
// from spending more tokens. A veto there returns an error from the LLM call
// and fails the task immediately (no REPAIRING — repairing would spend more).
// This is what a cost gate hooks into: read ev.Tokens, and once the budget is
// exhausted, refuse every further call.
type HookPoint string

const (
	HookBeforeEdit HookPoint = "before_edit"
	HookAfterEdit  HookPoint = "after_edit"
	HookBeforeTest HookPoint = "before_test"
	HookAfterTest  HookPoint = "after_test"
	HookBeforeChat HookPoint = "before_chat"
	HookOnError    HookPoint = "on_error"
)

// Hook is a user-supplied callback run at a HookPoint. It receives the same
// structured Event that Session.Log appends, so a hook is both a governance
// gate (return error to veto) and an observability sink.
type Hook func(ctx context.Context, ev Event) error

// fire runs every hook registered at point until one returns an error. Every
// hook event is stamped with the session's cumulative token cost, so any
// governance hook — a before_chat cost gate above all — can read ev.Tokens
// instead of tracking spend itself.
func (a *Agent) fire(ctx context.Context, point HookPoint, ev Event) error {
	ev.Tokens = a.sess.Usage.TotalTokens
	for _, h := range a.hooks[point] {
		if err := h(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}
