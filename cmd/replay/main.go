// Command replay renders a finished agent run's event log back into a
// readable timeline.
//
//	replay <result.json>        # text timeline (default)
//	replay -json <result.json>  # raw JSON event log
//
// The input is a serialized agent.Result (taskId/state/rounds/toolCalls/
// note/events), such as the one the CLI agent prints on completion. With no
// argument it runs a short mock example so the tool is demo-able without
// any network or fixtures on hand.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/zlx05/go-coding/internal/agent"
	"github.com/zlx05/go-coding/internal/replay"
)

func main() {
	asJSON := flag.Bool("json", false, "output the raw JSON event log instead of a text timeline")
	flag.Parse()

	path := flag.Arg(0)
	res := agent.Result{}
	if path == "" {
		res = demoResult()
	} else {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "replay:", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(b, &res); err != nil {
			fmt.Fprintln(os.Stderr, "replay: parse result:", err)
			os.Exit(1)
		}
	}

	if *asJSON {
		b, err := replay.JSON(res.Events)
		if err != nil {
			fmt.Fprintln(os.Stderr, "replay:", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}
	fmt.Print(replay.Build(res).String())
}

// demoResult manufactures a small event history so `replay` with no args
// shows what a run looks like.
func demoResult() agent.Result {
	base := agent.StatePlanning
	steps := []struct {
		st   agent.State
		tool string
		dtl  string
	}{
		{base, "", "task start"},
		{agent.StatePlanning, "explore", "## list_dir\nmathops.go"},
		{agent.StatePlanning, "", "plan: 1 steps"},
		{agent.StateEditing, "edit_file", "mathops.go: fix Add to sum"},
		{agent.StateExecuting, "run_test", "ok fixadd 1.002s"},
		{agent.StateDone, "", "tests passed"},
	}
	events := make([]agent.Event, 0, len(steps))
	t := time.Now()
	for i, s := range steps {
		events = append(events, agent.Event{
			Time:   t.Add(time.Duration(i) * 250 * time.Millisecond),
			State:  s.st,
			Tool:   s.tool,
			Detail: s.dtl,
		})
	}
	return agent.Result{
		TaskID:     "task-demo",
		State:      agent.StateDone,
		Rounds:     1,
		ToolCalls:  3,
		TestOutput: "ok fixadd 1.002s",
		Note:       "tests passed",
		Events:     events,
	}
}
