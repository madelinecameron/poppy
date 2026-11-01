package hub

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"path"
	"testing/fstest"

	"github.com/madelinecameron/poppy/internal/gitx"
)

var errReadOnlyCopy = errors.New("this hub is a read-only copy another poppy keeps; publish from that poppy")

// OpenCopy returns a read-only view of a hub clone that another poppy keeps
// on this machine, so a service can read the fleet without a clone, a key or
// a remote of its own.
//
// Reads see the copy's last commit, taken whole into memory. The owning
// poppy rewrites its working tree with a reset on every cycle, and a reader
// walking that tree could see one machine's files from before the reset and
// another's from after. A commit is never half written. Nothing in the copy
// is written: no fetch, no index refresh, no lock.
func OpenCopy(ctx context.Context, dir string) (*Store, error) {
	v := &copyView{git: gitx.New(dir), dir: dir}
	if err := v.refresh(ctx); err != nil {
		return nil, fmt.Errorf("read hub copy %s: %w", dir, err)
	}
	return &Store{Dir: dir, copy: v}, nil
}

type copyView struct {
	git   *gitx.Runner
	dir   string
	head  string
	files fstest.MapFS
}

// refresh takes the copy's current commit when it has moved. On failure the
// previous commit keeps answering, and the error says the view may be stale.
func (v *copyView) refresh(ctx context.Context) error {
	head, err := v.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return errors.New("no commit yet: the poppy that keeps it has never published")
	}
	if err != nil {
		return err
	}
	if head == v.head {
		return nil
	}
	archive, err := v.git.Bytes(ctx, v.args("archive", "--format=tar", head)...)
	if err != nil {
		return err
	}
	files, err := untar(archive)
	if err != nil {
		return fmt.Errorf("unpack %s: %w", head, err)
	}
	v.head, v.files = head, files
	return nil
}

// args lets git read a copy owned by another account. Git refuses such a
// repository as dubious unless it is named safe, and a reader is exactly the
// account that does not own it.
func (v *copyView) args(args ...string) []string {
	return append([]string{"-c", "safe.directory=" + v.dir}, args...)
}

func (v *copyView) run(ctx context.Context, args ...string) (string, error) {
	return v.git.Run(ctx, v.args(args...)...)
}

func untar(data []byte) (fstest.MapFS, error) {
	files := fstest.MapFS{}
	r := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(h.Name)
		if !fs.ValidPath(name) {
			return nil, fmt.Errorf("unsafe path %q", h.Name)
		}
		body, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		files[name] = &fstest.MapFile{Data: body, Mode: 0o644}
	}
}
