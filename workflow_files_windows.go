//go:build windows

package main

// Go does not expose a portable directory flush on Windows. File contents are
// flushed before replacement; os.Rename provides the platform's replacement
// semantics.
func syncParentDirectory(string) error {
	return nil
}
