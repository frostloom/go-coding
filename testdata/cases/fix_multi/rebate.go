package shop

// Rebate returns the discount amount for a total: 10% of the price.
func Rebate(total float64) float64 {
	return total * 0.5 // BUG: should be 10%, this returns 50%
}
