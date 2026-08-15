package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseSteps parses the LLM's JSON response into a step list. It is robust
// to the ways real models deviate from "STRICT JSON only": markdown code
// fences, multiple JSON objects concatenated in one reply, and — the common
// small-model failure — malformed JSON (stray braces, trailing commas, raw
// control chars). It first tries schema-aware extraction (find the "steps"
// array by key), falls back to the first balanced object, and repairs the
// result before unmarshal.
func parseSteps(content string) ([]Step, error) {
	stripped := stripCodeFence(content)
	jsonStr, ok := extractStepsArray(stripped)
	if !ok {
		jsonStr, ok = extractFirstJSON(stripped)
	}
	if !ok {
		return nil, fmt.Errorf("agent: no JSON object in LLM reply")
	}
	var out struct {
		Steps []Step `json:"steps"`
	}
	if err := json.Unmarshal([]byte(repairJSON(jsonStr)), &out); err != nil {
		return nil, fmt.Errorf("agent: parse plan JSON: %w", err)
	}
	if len(out.Steps) == 0 {
		return nil, fmt.Errorf("agent: LLM returned empty plan")
	}
	return out.Steps, nil
}

// planFormatReminder is the format-correction message fed back when the
// producer's reply isn't the STRICT JSON protocol. In executor mode the steps
// shape is the OUTLINE variant (file + desc + acceptance, no content); the
// classic mode keeps the full-content plan. The explore and call_tool shapes
// are identical in both.
func planFormatReminder(executor bool, err error) string {
	stepsShape := `{"steps":[{"file":"relative/path","content":"FULL NEW FILE CONTENT","desc":"what and why"}]} (the plan to edit)`
	if executor {
		stepsShape = `{"steps":[{"file":"relative/path","desc":"what and why","accept":"how correctness is verified"}]} (the outline plan — a worker executor implements each step, do NOT inline file content)`
	}
	return "Your previous reply was not valid JSON — you wrote prose or malformed JSON instead of the protocol (" + err.Error() + "). You MUST reply with STRICT JSON only, exactly ONE of these three shapes: " +
		`{"explore":[{"op":"..."}]} (to gather context first), ` +
		`{"call_tool":{"name":"<tool>","args":{...}}} (to invoke an external tool — if your previous intent was to call one, restate that call cleanly and completely), ` +
		"or " + stepsShape + ". Restate your intent as exactly one of these and nothing else."
}

// extractStepsArray locates the "steps" key in a reply and returns the
// balanced array that follows it, wrapped in a minimal {"steps":...} object.
// It is more tolerant than generic object extraction: preamble text, trailing
// garbage, and malformed braces OUTSIDE the steps value are all ignored,
// because we key on the protocol's schema rather than the reply's shape. This
// is what lets a local 7B model — which corrupts the wrapper but keeps the
// steps array intact — still drive the agent.
func extractStepsArray(s string) (string, bool) {
	idx := strings.Index(s, `"steps"`)
	if idx < 0 {
		return "", false
	}
	after := idx + len(`"steps"`)
	b := strings.IndexByte(s[after:], '[')
	if b < 0 {
		return "", false
	}
	start := after + b
	depth := 0
	inStr, esc := false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return `{"steps":` + s[start : i+1] + `}`, true
			}
		}
	}
	return "", false
}

// repairJSON fixes the JSON mistakes small models (e.g. local 7B) make that
// would otherwise fail json.Unmarshal. It is intentionally conservative —
// applied only to the already-extracted first object, and only fixes syntax
// that is unambiguously a typo:
//
//	"{\"a\":1,}"      → "{\"a\":1}"    (trailing comma in object)
//	"[\"x\",]"        → "[\"x\"]"      (trailing comma in array)
//	"\"raw\ttab\""    → "\"raw\\ttab\"" (raw control char inside a string)
//
// Trailing garbage after the object is already handled by extractFirstJSON.
func repairJSON(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				b.WriteByte(c)
				continue
			}
			if c == '\\' {
				esc = true
				b.WriteByte(c)
				continue
			}
			if c == '"' {
				inStr = false
				b.WriteByte(c)
				continue
			}
			// Raw control characters are illegal inside JSON strings. Escape
			// them (Qwen emits literal tabs/newlines in code content).
			if c < 0x20 {
				b.WriteString(escapeControl(c))
				continue
			}
			b.WriteByte(c)
			continue
		}
		// Outside a string: structural characters.
		switch c {
		case '"':
			inStr = true
			b.WriteByte(c)
		case ',':
			// A comma immediately before '}' or ']' is a trailing comma: drop
			// it AND any whitespace after it (it has no preceding element).
			j := i + 1
			for j < len(s) && isJSONSpace(s[j]) {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				i = j - 1 // skip comma + trailing whitespace; next write is the bracket
				continue
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func escapeControl(c byte) string {
	switch c {
	case '\t':
		return `\t`
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	default:
		return fmt.Sprintf(`\u%04x`, c)
	}
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}

// extractFirstJSON returns the first complete JSON object in s, starting at
// the first '{', and ignores any trailing content — extra JSON blobs, a
// "Here is my plan:" preamble, or a closing code fence that didn't match.
// It tracks string literals and escapes so a '}' inside a string does not
// prematurely end the object. Returns ok=false if no balanced object exists.
func extractFirstJSON(s string) (string, bool) {
	s = strings.TrimSpace(s)
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			switch c {
			case '\\':
				esc = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}
