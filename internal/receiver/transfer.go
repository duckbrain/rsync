package receiver

import (
	"fmt"
	"io/fs"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/internal/log"
	"github.com/gokrazy/rsync/internal/progress"
	"github.com/gokrazy/rsync/internal/rsyncopts"
	"github.com/gokrazy/rsync/internal/rsyncos"
	"github.com/gokrazy/rsync/internal/rsyncwire"
)

// TransferOpts is a subset of Opts which is required for implementing a receiver.
type TransferOpts struct {
	Verbose  bool
	DryRun   bool
	Server   bool
	Progress bool

	DeleteMode        bool
	PreserveGid       bool
	PreserveUid       bool
	PreserveLinks     bool
	PreservePerms     bool
	PreserveDevices   bool
	PreserveSpecials  bool
	PreserveTimes     bool
	PreserveHardlinks bool
	IgnoreTimes       bool
	AlwaysChecksum    bool
	DoFsync           bool
	KeepPartial       bool
	SparseFiles       bool

	InfoGTE  func(rsyncopts.InfoLevel, uint16) bool
	DebugGTE func(rsyncopts.DebugLevel, uint16) bool
}

type Transfer struct {
	// config
	Logger   log.Logger
	Opts     *TransferOpts
	Dest     string
	DestRoot rsync.DestRoot
	Env      *rsyncos.Env
	Progress progress.Printer
	// ReportError, if set, tells the client about a file that could not be
	// created. The transfer goes on without it.
	ReportError func(msg string)

	// state
	Conn         *rsyncwire.Conn
	Seed         int32
	IOErrors     int32
	Users        map[int32]mapping
	Groups       map[int32]mapping
	defaultPerms fs.FileMode
	retouchDirs  []retouchDir
}

type retouchDir struct {
	f    *File
	perm fs.FileMode
}

func (rt *Transfer) listOnly() bool { return rt.Dest == "" }

// fileError reports that f could not be created and goes on, as tridge rsync
// does for a symlink or device the destination cannot hold.
func (rt *Transfer) fileError(op string, f *File, err error) {
	rt.IOErrors++
	msg := fmt.Sprintf("%s %q failed: %v", op, f.Name, err)
	rt.Logger.Printf("%s", msg)
	if rt.ReportError != nil {
		rt.ReportError(msg)
	}
}
