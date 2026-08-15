package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRepairJSONTrailingComma verifies repairJSON fixes the exact malformed
// JSON a small local model (qwen2.5:7b) actually produced in a live run.
func TestRepairJSONTrailingComma(t *testing.T) {
	cases := []struct{ in, want string }{
		// object trailing comma
		{`{"steps":[{"file":"a.go","content":"x",}]}`, `{"steps":[{"file":"a.go","content":"x"}]}`},
		// array trailing comma
		{`{"steps":[{"file":"a.go","content":"x"},]}`, `{"steps":[{"file":"a.go","content":"x"}]}`},
		// comma + whitespace before closing bracket
		{`{"steps":[{"file":"a.go","content":"x"}, ]}`, `{"steps":[{"file":"a.go","content":"x"}]}`},
		// valid JSON must be untouched
		{`{"steps":[{"file":"a.go","content":"x"}]}`, `{"steps":[{"file":"a.go","content":"x"}]}`},
	}
	for _, c := range cases {
		got := repairJSON(c.in)
		if got != c.want {
			t.Errorf("repairJSON(%q) = %q, want %q", c.in, got, c.want)
		}
		var probe struct{ Steps []Step }
		if err := json.Unmarshal([]byte(got), &probe); err != nil {
			t.Errorf("repaired output should be valid JSON: %v (in=%q)", err, c.in)
		}
	}
}

// TestExtractStepsArraySurvivesMalformedWrappers: the exact failure shapes
// qwen2.5:7b produced in live runs — stray braces, misplaced brackets,
// trailing prose — around an intact "steps" value. Schema-aware extraction
// must recover the steps array regardless of how the wrapper is corrupted.
func TestExtractStepsArraySurvivesMalformedWrappers(t *testing.T) {
	valid := `{"steps":[{"file":"mathops.go","content":"package fixadd\n\nfunc Add(a, b int) int {\n\treturn a + b\n}","desc":"fix Add"}]}`
	shapes := []string{
		// extra '}' after the array (observed: "invalid character '}' after array element")
		`{"steps":[{"file":"mathops.go","content":"x","desc":"d"}]}}`,
		// stray array after the steps value (observed: "invalid character '[' after key:value pair")
		`{"steps":[{"file":"mathops.go","content":"x","desc":"d"}],["x"]}`,
		// trailing prose + concatenated garbage
		`{"steps":[{"file":"mathops.go","content":"x","desc":"d"}]}\n\nI have updated it. {"steps":[]}`,
	}
	for _, shape := range shapes {
		steps, err := parseSteps(shape)
		if err != nil {
			t.Errorf("parseSteps(%q) should recover the steps array, got err: %v", shape, err)
			continue
		}
		if len(steps) == 0 || steps[0].File != "mathops.go" {
			t.Errorf("parseSteps(%q) wrong steps: %+v", shape, steps)
		}
	}
	_ = valid
}

// TestExtractStepsArrayBalancesNestedArrays: a step whose content contains
// brackets (Go code) must not break array balancing.
func TestExtractStepsArrayBalancesNestedArrays(t *testing.T) {
	in := `{"steps":[{"file":"x.go","content":"func F() {\n\tif a > 1 { return }\n}","desc":"d"}]}`
	steps, err := parseSteps(in)
	if err != nil {
		t.Fatalf("parseSteps with braces in content error = %v", err)
	}
	if len(steps) != 1 || steps[0].File != "x.go" {
		t.Errorf("unexpected steps: %+v", steps)
	}
}

// TestRepairJSONRawControlChar: a small model sometimes emits a literal tab
// or newline INSIDE a JSON string (unscaped). Go's json rejects raw control
// chars in strings, so repairJSON must escape them.
func TestRepairJSONRawControlChar(t *testing.T) {
	// "content": "package x
	// 	func F()"  — raw newline + raw tab inside the string.
	in := "{\"steps\":[{\"file\":\"x.go\",\"content\":\"package x\n\tfunc F()\"}]}"
	var probe struct{ Steps []Step }
	if err := json.Unmarshal([]byte(in), &probe); err == nil {
		t.Fatal("precondition: the raw-control-char JSON must be invalid")
	}
	got := repairJSON(in)
	if err := json.Unmarshal([]byte(got), &probe); err != nil {
		t.Fatalf("repairJSON should escape control chars, still invalid: %v\n got=%q", err, got)
	}
	if len(probe.Steps) != 1 || probe.Steps[0].Content != "package x\n\tfunc F()" {
		t.Errorf("escaped content should decode back to original, got %+v", probe.Steps)
	}
}

// TestParseStepsRepairsSmallModelJSON runs the full parse path (extract →
// repair → unmarshal) against a real qwen-style reply with trailing garbage.
func TestParseStepsRepairsSmallModelJSON(t *testing.T) {
	reply := `{"steps":[{"file":"mathops.go","content":"package fixadd\n\nfunc Add(a, b int) int {\n\treturn a + b\n}","desc":"fix Add"}]}\n\nI have updated the Add function to correctly return the sum.`
	steps, err := parseSteps(reply)
	if err != nil {
		t.Fatalf("parseSteps should repair and accept, got err: %v", err)
	}
	if len(steps) != 1 || steps[0].File != "mathops.go" {
		t.Errorf("unexpected steps: %+v", steps)
	}
	if !strings.Contains(steps[0].Content, "return a + b") {
		t.Errorf("content should contain the fix, got: %s", steps[0].Content)
	}
}

// TestParseStepsAcceptField verifies the P22 OUTLINE protocol: a step that
// carries an acceptance criterion (file + desc + accept, no content) parses
// into Accept, while the classic content protocol leaves Accept empty.
func TestParseStepsAcceptField(t *testing.T) {
	outline, err := parseSteps(`{"steps":[{"file":"mathops.go","desc":"fix Add to sum","accept":"Add must return the sum"}]}`)
	if err != nil {
		t.Fatalf("parseSteps(outline) error = %v", err)
	}
	if len(outline) != 1 {
		t.Fatalf("expected 1 step, got %d", len(outline))
	}
	if outline[0].Accept != "Add must return the sum" {
		t.Errorf("Accept = %q, want the acceptance criterion", outline[0].Accept)
	}
	if outline[0].Content != "" {
		t.Errorf("outline step must carry no content, got %q", outline[0].Content)
	}
	content, err := parseSteps(`{"steps":[{"file":"mathops.go","content":"package x","desc":"d"}]}`)
	if err != nil {
		t.Fatalf("parseSteps(content) error = %v", err)
	}
	if content[0].Accept != "" {
		t.Errorf("content-protocol step must have empty Accept, got %q", content[0].Accept)
	}
}

// TestParseStepsHandlesConcatenatedJSON: a real live model (gpt-5.6-luna)
// returned four explore JSON objects concatenated in one reply:
//
//	{"explore":[...]}{"explore":[...]}{"explore":[...]}	{"explore":[...]}
//
// parseSteps must not fail on that — and the parse must take the FIRST
// complete object, not blow up on the second '{'. This is the robustness
// test for live-mode model nondeterminism.
func TestParseStepsHandlesConcatenatedJSON(t *testing.T) {
	input := `{"explore":[{"op":"list_dir"},{"op":"grep","pattern":"Add"}]}{"explore":[{"op":"list_dir"}]}`
	exp, ok := parseExplore(input)
	if !ok {
		t.Fatal("parseExplore should succeed on concatenated JSON (first object wins)")
	}
	if len(exp) != 2 || exp[0].Op != "list_dir" || exp[1].Pattern != "Add" {
		t.Errorf("expected first object's 2 ops, got %+v", exp)
	}
}

// TestParseStepsToleratesTrailingText: some models append commentary after
// the JSON despite "STRICT JSON only". The first object must still parse.
func TestParseStepsToleratesTrailingText(t *testing.T) {
	input := `{"steps":[{"file":"a.go","content":"x","desc":"d"}]}\nHere is my reasoning and I will also add more text.`
	steps, err := parseSteps(input)
	if err != nil {
		t.Fatalf("parseSteps(trailing text) error = %v", err)
	}
	if len(steps) != 1 || steps[0].File != "a.go" {
		t.Errorf("expected 1 step a.go, got %+v", steps)
	}
}

// TestExtractFirstJSONSkipsBracesInsideStrings: a '}' or '{' inside a JSON
// string must not be treated as a structural brace.
func TestExtractFirstJSONSkipsBracesInsideStrings(t *testing.T) {
	// The content contains "foo { bar }" inside a string — must not end early.
	input := `{"steps":[{"file":"x.go","content":"fmt.Println(\"{}\")"}]} trailing`
	got, ok := extractFirstJSON(input)
	if !ok {
		t.Fatal("extractFirstJSON should succeed")
	}
	// The extracted object must be exactly the JSON, no trailing text.
	if got[len(got)-1] != '}' {
		t.Errorf("extracted JSON should end with '}', got %q", got)
	}
	// It must still parse as a step.
	if _, err := parseSteps(input); err != nil {
		t.Errorf("parseSteps with braces in string error = %v", err)
	}
}

// TestExtractFirstJSONHandlesCodeFence: markdown fences around the JSON are
// stripped before extraction.
func TestExtractFirstJSONHandlesCodeFence(t *testing.T) {
	input := "```json\n{\"steps\":[{\"file\":\"a.go\",\"content\":\"x\",\"desc\":\"d\"}]}\n```"
	steps, err := parseSteps(input)
	if err != nil {
		t.Fatalf("parseSteps(code fence) error = %v", err)
	}
	if len(steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(steps))
	}
}

// TestExtractFirstJSONNoObject: a reply with no '{' at all is not parseable.
func TestExtractFirstJSONNoObject(t *testing.T) {
	if _, ok := extractFirstJSON("sorry, I cannot help"); ok {
		t.Error("expected no JSON object for prose-only reply")
	}
	if _, err := parseSteps("just words, no json"); err == nil {
		t.Error("expected error for prose-only reply")
	}
}
