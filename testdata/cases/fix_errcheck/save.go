package fixerrcheck

import "os"

// Save writes data to path. It must return a non-nil error if the write
// fails.
func Save(path string, data []byte) error {
	_ = os.WriteFile(path, data, 0o644) // bug: the error is swallowed
	return nil
}
