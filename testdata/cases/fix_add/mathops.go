package fixadd

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a - b // bug: should be +
}
