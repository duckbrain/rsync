package receiver

import (
	"bytes"
	"path/filepath"

	"github.com/gokrazy/rsync"
)

type pendingFile struct {
	root    rsync.DestRoot
	tmpname string
	fn      string
	f       rsync.PendingFile
	sync    bool
	sparse  bool
	size    int64 // bytes received so far (only tracked for sparse files)
	renamed bool
}

func newPendingFile(root rsync.DestRoot, fn string, sync, sparse bool) (*pendingFile, error) {
	tmpname, f, err := root.CreateTemp(fn, 0o600)
	if err != nil {
		return nil, err
	}
	return &pendingFile{
		root:    root,
		tmpname: tmpname,
		fn:      fn,
		f:       f,
		sync:    sync,
		sparse:  sparse,
	}, nil
}

func (p *pendingFile) Name() string {
	return p.fn
}

// zeroBlock is the shortest run of zeros that is worth turning into a hole.
//
// rsync/fileio.c:SPARSE_WRITE_SIZE
var zeroBlock = make([]byte, 1024)

// Skip over zeros instead of writing them, leaving a hole in the file.
//
// rsync/fileio.c:write_sparse
func (p *pendingFile) sparseWrite(buf []byte) (n int, _ error) {
	rest := buf
	for len(rest) > 0 {
		data := bytes.TrimLeft(rest, "\x00")
		p.size += int64(len(rest) - len(data))
		rest = nil
		if idx := bytes.Index(data, zeroBlock); idx > -1 {
			data, rest = data[:idx], data[idx:]
		}
		if _, err := p.f.WriteAt(data, p.size); err != nil {
			return 0, err
		}
		p.size += int64(len(data))
	}
	return len(buf), nil
}

func (p *pendingFile) Write(buf []byte) (n int, _ error) {
	if p.sparse {
		return p.sparseWrite(buf)
	}
	return p.f.Write(buf)
}

func (p *pendingFile) CloseAtomicallyReplace() error {
	if p.sparse {
		// The file might end in a hole: extend it to its full size.
		//
		// rsync/fileio.c:sparse_end
		if err := p.f.Truncate(p.size); err != nil {
			return err
		}
	}
	if p.sync {
		// fsync was requested
		if err := p.f.Sync(); err != nil {
			return err
		}
	}
	if err := p.f.Close(); err != nil {
		return err
	}
	if err := p.root.Rename(p.tmpname, p.fn); err != nil {
		return err
	}
	p.renamed = true
	return nil
}

func (p *pendingFile) Cleanup() error {
	if p.renamed {
		return nil // CloseAtomicallyReplace succeeded.
	}
	err := p.f.Close()
	if err := p.root.Remove(p.tmpname); err != nil {
		return err
	}
	return err
}

func (p *pendingFile) KeepAsPartial(partialName string) error {
	if p.renamed {
		return nil // CloseAtomicallyReplace succeeded.
	}
	err := p.f.Close()
	if err := p.root.MkdirAll(filepath.Dir(partialName), 0700); err != nil {
		return err
	}
	if err := p.root.Rename(p.tmpname, partialName); err != nil {
		return err
	}
	return err
}
