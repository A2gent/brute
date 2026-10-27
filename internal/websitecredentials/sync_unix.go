//go:build !windows

package websitecredentials

import "os"

// syncDir makes renames and newly created entries in dir durable. Without it a
// crash after rename can lose the directory entry even though file data was synced.
func syncDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
