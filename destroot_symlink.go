//go:build linux || darwin

package rsync

import (
	"os"

	"github.com/google/renameio/v2"
)

func symlinkRoot(root *os.Root, oldname, newname string) error {
	return renameio.SymlinkRoot(root, oldname, newname)
}
