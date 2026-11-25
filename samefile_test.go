package gbash

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A host-backed filesystem wraps its file information, which os.SameFile refuses, so -ef must tell two names of
// one file by device and inode.
func TestSameFileOperatorOnHostBackedFileSystems(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"a", "c"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("a"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(dir, "a"), filepath.Join(dir, "hard")); err != nil {
		t.Fatal(err)
	}
	const checks = `test a -ef a && echo test; [ a -ef hard ] && echo bracket; [[ a -ef hard ]] && echo double; [[ a -ef c ]] || echo differ`
	for name, tc := range map[string]struct {
		fs  FileSystemConfig
		dir string
	}{
		"read-write directory": {ReadWriteDirectoryFileSystem(dir, ReadWriteDirectoryOptions{}), "/"},
		"host directory":       {HostDirectoryFileSystem(dir, HostDirectoryOptions{MountPoint: "/w"}), "/w"},
	} {
		rt, err := New(WithFileSystem(tc.fs))
		if err != nil {
			t.Fatal(err)
		}
		res, err := rt.Run(context.Background(), &ExecutionRequest{Script: "cd " + tc.dir + "; " + checks})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := res.Stdout, "test\nbracket\ndouble\ndiffer\n"; got != want {
			t.Errorf("%s: got %q, want %q (stderr %q)", name, got, want, res.Stderr)
		}
	}
}
