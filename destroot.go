package rsync

import (
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"strconv"
	"time"
)

// DestRoot is the directory a receiver writes into. Names are slash-separated
// and relative to the root, as with [os.Root], and must not escape it.
//
// [OSRoot] implements DestRoot over a local directory. Other implementations
// let a program receive into storage that is not a local directory, or that
// must see every change (to keep an index or a quota in step), without
// staging the transfer on disk first.
type DestRoot interface {
	// Lstat returns information about the named file without following a
	// final symlink.
	Lstat(name string) (fs.FileInfo, error)

	// Open opens the named file for reading. The receiver reads an existing
	// file sequentially to compute checksums and at random offsets to copy
	// matching blocks.
	Open(name string) (BasisFile, error)

	// CreateTemp creates a new, uniquely named file to receive the contents
	// of the named file, in the same directory, and returns the temporary
	// file's name. The receiver renames it over name when the transfer is
	// complete, or removes it (or renames it to a partial name) when not.
	CreateTemp(name string, perm fs.FileMode) (tmpname string, f PendingFile, err error)

	Rename(oldname, newname string) error
	Remove(name string) error
	RemoveAll(name string) error
	MkdirAll(name string, perm fs.FileMode) error
	Readlink(name string) (string, error)

	// Symlink creates newname as a symbolic link to oldname, replacing any
	// existing file at newname.
	Symlink(oldname, newname string) error

	Chmod(name string, mode fs.FileMode) error
	Chtimes(name string, atime, mtime time.Time) error
	Lchown(name string, uid, gid int) error

	// FS returns the root as a read-only file system, which --delete walks to
	// find files the sender no longer has.
	FS() fs.FS

	// Sub returns the root of the named directory within this one.
	Sub(name string) (DestRoot, error)
}

// BasisFile is an existing file the receiver reads to build its new version.
type BasisFile interface {
	io.Reader
	io.ReaderAt
	io.Closer
	Stat() (fs.FileInfo, error)
}

// PendingFile is a file being received.
type PendingFile interface {
	io.Writer
	io.WriterAt
	io.Closer
	Truncate(size int64) error
	Sync() error
}

// OSRoot is a [DestRoot] backed by a local directory.
type OSRoot struct {
	*os.Root
}

var _ DestRoot = OSRoot{}

func (r OSRoot) Open(name string) (BasisFile, error) {
	f, err := r.Root.Open(name)
	if err != nil {
		// Return an untyped nil: a nil *os.File in an interface is not nil.
		return nil, err
	}
	return f, nil
}

// CreateTemp creates a randomly named file beside name. It is similar to
// os.CreateTemp except that the file permissions can be controlled. The name
// is always the base name with a leading dot and a random number suffixed.
func (r OSRoot) CreateTemp(name string, perm fs.FileMode) (string, PendingFile, error) {
	dir, base := splitDir(name)
	prefix := dir + "." + base

	for attempt := 0; ; {
		// Generate a reasonably random name which is unlikely to already
		// exist. O_EXCL ensures that existing files generate an error.
		tmpname := prefix + strconv.FormatInt(rand.Int64(), 10)

		f, err := r.Root.OpenFile(tmpname, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if err == nil {
			return tmpname, f, nil
		}
		if !os.IsExist(err) {
			return "", nil, err
		}

		if attempt++; attempt > 10000 {
			return "", nil, &os.PathError{
				Op:   "tempfile",
				Path: tmpname,
				Err:  os.ErrExist,
			}
		}
	}
}

func (r OSRoot) Symlink(oldname, newname string) error {
	return symlinkRoot(r.Root, oldname, newname)
}

func (r OSRoot) FS() fs.FS { return r.Root.FS() }

func (r OSRoot) Sub(name string) (DestRoot, error) {
	sub, err := r.Root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return OSRoot{sub}, nil
}

// splitDir splits a slash-separated name into its directory, with a trailing
// slash unless empty, and its base.
func splitDir(name string) (dir, base string) {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[:i+1], name[i+1:]
		}
	}
	return "", name
}
