package harness

import (
	"encoding/json"
	"os"
)

// Report is the machine-readable snapshot of one harness run: mode
// ("mock" | "live"), per-case results and the aggregated summary. It is
// what lets iteration compare runs over time and track real measured
// numbers as a regression baseline.
type Report struct {
	Mode    string        `json:"mode"`
	Cases   []CaseResult  `json:"cases"`
	Summary Summary       `json:"summary"`
}

// WriteJSON persists case results to disk for post-run analysis.
func WriteJSON(results []CaseResult, path string) error {
	return writeJSON(Report{Mode: "cases", Cases: results, Summary: Summary{}}, path)
}

// WriteReport persists a full run (mode + cases + summary) as JSON.
func WriteReport(path, mode string, results []CaseResult, s Summary) error {
	return writeJSON(Report{Mode: mode, Cases: results, Summary: s}, path)
}

func writeJSON(v any, path string) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
