//go:build windows

package rsync

import "os"

func symlinkRoot(destroot *os.Root, oldname, newname string) error {
	if err := destroot.Remove(newname); err != nil && !os.IsNotExist(err) {
		return err
	}
	return destroot.Symlink(oldname, newname)
}
