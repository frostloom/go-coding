package mcpserver

import (
	"fmt"
	"strings"
)

// unifiedDiff renders oldText → newText as a line-based diff (a simplified
// unified format: space=context, -=removed, +=added, with one @@ header).
// It is what edit_file returns as a preview so the user (and the host LLM)
// sees exactly what would change before anything is written.
//
// Implemented with a classic LCS (longest-common-subsequence) DP table —
// small and dependency-free, matching the project's "from scratch" ethos.
func unifiedDiff(oldText, newText string) string {
	a := splitLines(oldText)
	b := splitLines(newText)

	// Guard against pathological inputs: beyond this, just summarize.
	const capLines = 4000
	if len(a) > capLines || len(b) > capLines {
		return fmt.Sprintf("(change too large to preview: %d -> %d lines)", len(a), len(b))
	}

	// LCS lengths: dp[i][j] = LCS of a[i:] and b[j:].
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "@@ -1,%d +1,%d @@\n", len(a), len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			buf.WriteString(" " + a[i] + "\n")
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			buf.WriteString("-" + a[i] + "\n")
			i++
		default:
			buf.WriteString("+" + b[j] + "\n")
			j++
		}
	}
	for ; i < len(a); i++ {
		buf.WriteString("-" + a[i] + "\n")
	}
	for ; j < len(b); j++ {
		buf.WriteString("+" + b[j] + "\n")
	}
	return buf.String()
}

// splitLines splits on \n, dropping a single trailing empty element so a
// final newline does not produce a phantom line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}
