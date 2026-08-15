package implorderedset

// Set keeps strings in insertion order with no duplicates: adding a value
// that is already present is a no-op. This is the "ordered set" data
// structure — distinct from a map alone because Values() must preserve the
// first-insertion order.
type Set struct {
	// TODO: implement
}

// New returns an empty set.
func New() *Set {
	return &Set{}
}

// Add inserts v if it is not already present. It reports whether v was newly
// added (false for a duplicate).
func (s *Set) Add(v string) bool {
	return false // TODO
}

// Contains reports whether v is in the set.
func (s *Set) Contains(v string) bool {
	return false // TODO
}

// Len returns the number of distinct values.
func (s *Set) Len() int {
	return 0 // TODO
}

// Values returns the values in insertion order.
func (s *Set) Values() []string {
	return nil // TODO
}
