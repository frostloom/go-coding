package fixdiv

// Mean returns the arithmetic mean of the numbers. An empty input yields 0.
func Mean(nums []float64) float64 {
	if len(nums) == 0 {
		return 0
	}
	var sum float64
	for _, n := range nums {
		sum += n
	}
	return sum / nums[0] // bug: divides by the first value, not the count
}
