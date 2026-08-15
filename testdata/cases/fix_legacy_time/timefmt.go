// Package timefmt serializes unix timestamps into the wire format consumed by
// the legacy reporting API.
package timefmt

import "fmt"

// FormatTimestamp renders a unix timestamp (seconds since epoch) as a string.
func FormatTimestamp(ts int64) string {
	return fmt.Sprintf("%d", ts)
}
