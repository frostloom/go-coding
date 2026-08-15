package shop

// Total sums the items and applies the discount.
func Total(items []float64) float64 {
	var sum float64
	for _, v := range items {
		sum += v
	}
	return ApplyDiscount(sum)
}
