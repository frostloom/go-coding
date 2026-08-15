package shop

// ApplyDiscount subtracts the rebate from the total.
func ApplyDiscount(total float64) float64 {
	return total - Rebate(total)
}
