package fixoffbyone

// SumTo returns 1 + 2 + ... + n. SumTo(0) returns 0.
func SumTo(n int) int {
	total := 0
	for i := 1; i < n; i++ { // bug: the loop stops one step early, skipping n
		total += i
	}
	return total
}
