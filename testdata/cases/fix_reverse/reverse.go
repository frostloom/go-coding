package fixreverse

// Reverse returns s with its characters reversed.
func Reverse(s string) string {
	if len(s) <= 1 {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := len(s) - 1; i > 0; i-- { // bug: loop stops at i>0, drops s[0]
		out = append(out, s[i])
	}
	return string(out)
}
