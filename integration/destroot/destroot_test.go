package destroot_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/internal/testlogger"
	"github.com/gokrazy/rsync/rsyncclient"
	"github.com/gokrazy/rsync/rsyncd"
)

// recordingRoot is a DestRoot that records the files it is asked to replace,
// so a test can tell that the receiver went through it and not around it.
type recordingRoot struct {
	rsync.OSRoot
	mu      *sync.Mutex
	renamed *[]string
}

func (r recordingRoot) Rename(oldname, newname string) error {
	r.mu.Lock()
	*r.renamed = append(*r.renamed, newname)
	r.mu.Unlock()
	return r.OSRoot.Rename(oldname, newname)
}

func (r recordingRoot) Sub(name string) (rsync.DestRoot, error) {
	sub, err := r.OSRoot.Sub(name)
	if err != nil {
		return nil, err
	}
	return recordingRoot{sub.(rsync.OSRoot), r.mu, r.renamed}, nil
}

func newRecordingRoot(t *testing.T, dir string) (recordingRoot, func() []string) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	var mu sync.Mutex
	var renamed []string
	return recordingRoot{rsync.OSRoot{Root: root}, &mu, &renamed}, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Sorted(slices.Values(renamed))
	}
}

// run connects an in-process client to module by pipes, as a program
// serving rsync over its own transport would.
func run(t *testing.T, module rsyncd.Module, clientArgs []string, opts []rsyncclient.Option, serverPath string, paths []string) {
	t.Helper()
	srv, err := rsyncd.NewServer([]rsyncd.Module{module}, rsyncd.DontRestrict(), rsyncd.WithStderr(testlogger.New(t)))
	if err != nil {
		t.Fatal(err)
	}
	cl, err := rsyncclient.New(clientArgs, append(opts, rsyncclient.DontRestrict(), rsyncclient.WithStderr(testlogger.New(t)))...)
	if err != nil {
		t.Fatal(err)
	}
	stdinrd, stdinwr, stdoutrd, stdoutwr := pipes(t)
	var wg sync.WaitGroup
	wg.Go(func() {
		defer stdoutwr.Close()
		conn := rsyncd.NewConnection(stdinrd, stdoutwr, "<pipe>")
		if err := srv.HandleConnArgs(t.Context(), conn, &module, cl.ServerCommandOptions(serverPath)); err != nil {
			t.Error(err)
		}
	})
	rw := &rsync.BothCloser{ReadCloser: stdoutrd, WriteCloser: stdinwr}
	if _, err := cl.Run(t.Context(), rw, paths); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

// pipes returns kernel pipes for the server's stdin and stdout. Unlike
// io.Pipe they buffer, as an SSH channel or a socket does, so a side that
// writes an error while the other is still writing does not deadlock.
func pipes(t *testing.T) (stdinrd, stdinwr, stdoutrd, stdoutwr *os.File) {
	t.Helper()
	stdinrd, stdinwr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutrd, stdoutwr, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, f := range []*os.File{stdinrd, stdinwr, stdoutrd, stdoutwr} {
			f.Close()
		}
	})
	return stdinrd, stdinwr, stdoutrd, stdoutwr
}

func writeFile(t *testing.T, name, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReceiveIntoRoot(t *testing.T) {
	src := t.TempDir()
	mtime := time.Date(2009, time.November, 10, 23, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(src, "a.txt"), "alpha", mtime)
	writeFile(t, filepath.Join(src, "dir", "b.txt"), "bravo", mtime)

	dest := t.TempDir()
	root, renamed := newRecordingRoot(t, dest)
	module := rsyncd.Module{Name: "dest", Root: root, Writable: true}

	run(t, module, []string{"-a"}, []rsyncclient.Option{rsyncclient.WithSender()}, "sub/", []string{src + "/"})

	if got := readFile(t, filepath.Join(dest, "sub", "a.txt")); got != "alpha" {
		t.Errorf("a.txt = %q, want alpha", got)
	}
	if got := readFile(t, filepath.Join(dest, "sub", "dir", "b.txt")); got != "bravo" {
		t.Errorf("dir/b.txt = %q, want bravo", got)
	}
	if got, want := renamed(), []string{"a.txt", "dir/b.txt"}; !slices.Equal(got, want) {
		t.Errorf("files replaced through the root = %q, want %q", got, want)
	}
	st, err := os.Stat(filepath.Join(dest, "sub", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(mtime) {
		t.Errorf("a.txt mtime = %v, want %v", st.ModTime(), mtime)
	}

	// A second run sends a.txt again against the copy already there, which
	// the receiver reads through the root.
	writeFile(t, filepath.Join(src, "a.txt"), "alpha and omega", mtime.Add(time.Hour))
	run(t, module, []string{"-a"}, []rsyncclient.Option{rsyncclient.WithSender()}, "sub/", []string{src + "/"})
	if got := readFile(t, filepath.Join(dest, "sub", "a.txt")); got != "alpha and omega" {
		t.Errorf("a.txt after update = %q", got)
	}
	if got, want := renamed(), []string{"a.txt", "a.txt", "dir/b.txt"}; !slices.Equal(got, want) {
		t.Errorf("files replaced through the root = %q, want %q", got, want)
	}
}

func TestSendFromRoot(t *testing.T) {
	served := t.TempDir()
	mtime := time.Date(2009, time.November, 10, 23, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(served, "hello.txt"), "world", mtime)
	root, _ := newRecordingRoot(t, served)
	module := rsyncd.Module{Name: "served", Root: root}

	dest := t.TempDir()
	run(t, module, []string{"-a"}, nil, "./", []string{dest + "/"})

	if got := readFile(t, filepath.Join(dest, "hello.txt")); got != "world" {
		t.Errorf("hello.txt = %q, want world", got)
	}
}

func TestReadOnlyRootRefusesWrites(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha", time.Now())
	root, renamed := newRecordingRoot(t, t.TempDir())
	module := rsyncd.Module{Name: "ro", Root: root}

	srv, err := rsyncd.NewServer([]rsyncd.Module{module}, rsyncd.DontRestrict(), rsyncd.WithStderr(testlogger.New(t)))
	if err != nil {
		t.Fatal(err)
	}
	cl, err := rsyncclient.New([]string{"-a"}, rsyncclient.WithSender(), rsyncclient.DontRestrict(), rsyncclient.WithStderr(testlogger.New(t)))
	if err != nil {
		t.Fatal(err)
	}
	stdinrd, stdinwr, stdoutrd, stdoutwr := pipes(t)
	errc := make(chan error, 1)
	go func() {
		defer stdoutwr.Close()
		defer stdinrd.Close()
		errc <- srv.HandleConnArgs(t.Context(), rsyncd.NewConnection(stdinrd, stdoutwr, "<pipe>"), &module, cl.ServerCommandOptions("./"))
	}()
	_, _ = cl.Run(t.Context(), &rsync.BothCloser{ReadCloser: stdoutrd, WriteCloser: stdinwr}, []string{src + "/"})
	if err := <-errc; err == nil {
		t.Error("a read-only module accepted a push")
	}
	if got := renamed(); len(got) != 0 {
		t.Errorf("read-only module replaced %q", got)
	}
}

func TestValidateRoot(t *testing.T) {
	root, _ := newRecordingRoot(t, t.TempDir())
	if _, err := rsyncd.NewServer([]rsyncd.Module{{Name: "both", Path: "/tmp", Root: root}}); err == nil {
		t.Error("a module with both Path and Root was accepted")
	}
	if _, err := rsyncd.NewServer([]rsyncd.Module{{Name: "rw", FS: fs.FS(os.DirFS("/")), Root: root, Writable: true}}, rsyncd.DontRestrict()); err != nil {
		t.Errorf("a writable module with FS and Root was refused: %v", err)
	}
}
