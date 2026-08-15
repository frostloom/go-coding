package queue

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/zlx05/go-coding/internal/agent"
	"github.com/zlx05/go-coding/internal/fsutil"
	"github.com/zlx05/go-coding/pkg/llm"
)

// Task is one unit of work carried by the queue: a requirement to satisfy in
// a workspace. The workspace itself is minted fresh per task (each task runs
// in its own isolated sandbox — no two tasks ever touch the same files).
type Task struct {
	ID          string `json:"id"`
	BaseRepo    string `json:"baseRepo"` // source repo/fixture to copy
	Requirement string `json:"requirement"`
	MaxRounds   int    `json:"maxRounds,omitempty"`
}

// NewTask builds a task with a generated id.
func NewTask(id, baseRepo, requirement string) Task {
	return Task{ID: id, BaseRepo: baseRepo, Requirement: requirement, MaxRounds: 5}
}

// Marshal encodes the task for the queue wire format.
func (t Task) Marshal() ([]byte, error) {
	return json.Marshal(t)
}

// DecodeTask parses a queue payload back into a Task.
func DecodeTask(payload []byte) (Task, error) {
	var t Task
	if err := json.Unmarshal(payload, &t); err != nil {
		return Task{}, err
	}
	if t.ID == "" || t.BaseRepo == "" || t.Requirement == "" {
		return Task{}, jsonMissingFields
	}
	return t, nil
}

// TaskResult is the measured outcome of one queued task.
type TaskResult struct {
	ID        string        `json:"id"`
	Passed    bool          `json:"passed"`
	Rounds    int           `json:"rounds"`
	ToolCalls int           `json:"toolCalls"`
	Tokens    int           `json:"tokens"`
	Duration  time.Duration `json:"durationNs"`
	Note      string        `json:"note,omitempty"`
}

// NewAgentTaskHandler returns a queue.Handler that runs each Task payload
// through the real agent loop in a fresh, isolated temp workspace. onResult
// (may be nil) receives each task's outcome for logging/metrics. The LLM
// client is shared across tasks — the session/context is per-task.
func NewAgentTaskHandler(lc llm.ChatClient, runTimeout time.Duration, onResult func(TaskResult)) Handler {
	return func(ctx context.Context, payload []byte) error {
		t, err := DecodeTask(payload)
		if err != nil {
			return err
		}
		res := runAgentTask(ctx, t, lc, runTimeout)
		if onResult != nil {
			onResult(res)
		}
		// A task that "fails" (agent didn't pass) is a completed-but-unhappy
		// outcome, not a handler crash — so it does NOT count as a worker
		// failure. Only setup errors return non-nil.
		return nil
	}
}

// runAgentTask executes one task end to end in a throwaway workspace:
// copy base repo → run the agent loop → measure. Returns a TaskResult.
func runAgentTask(ctx context.Context, t Task, lc llm.ChatClient, runTimeout time.Duration) TaskResult {
	fail := func(note string) TaskResult {
		return TaskResult{ID: t.ID, Note: note}
	}

	ws, err := os.MkdirTemp("", "go-coding-task-*")
	if err != nil {
		return fail("setup: " + err.Error())
	}
	defer os.RemoveAll(ws)

	if err := fsutil.CopyDir(t.BaseRepo, ws); err != nil {
		return fail("copy base repo: " + err.Error())
	}

	maxRounds := t.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 5
	}
	a := agent.New(lc, agent.Config{
		RepoPath:    ws,
		Requirement: t.Requirement,
		MaxRounds:   maxRounds,
		RunTimeout:  runTimeout,
	})
	start := time.Now()
	res := a.Run(ctx)

	return TaskResult{
		ID:        t.ID,
		Passed:    res.Passed(),
		Rounds:    res.Rounds,
		ToolCalls: res.ToolCalls,
		Tokens:    res.Usage.TotalTokens,
		Duration:  time.Since(start),
		Note:      res.Note,
	}
}

var jsonMissingFields = &taskError{"task: missing id/baseRepo/requirement"}

type taskError struct{ msg string }

func (e *taskError) Error() string { return e.msg }
