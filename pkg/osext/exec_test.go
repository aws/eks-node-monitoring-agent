package osext_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/aws/eks-node-monitoring-agent/pkg/osext"
)

func TestLookPath(t *testing.T) {
	// directories should fail the lookup.
	t.Run("Directory", func(t *testing.T) {
		tempRoot := t.TempDir()

		assert.NoError(t, os.MkdirAll(filepath.Join(tempRoot, "foo"), 0755))

		dirCmd := osext.NewExec(tempRoot).Command("foo")

		assert.ErrorIs(t, dirCmd.Err, exec.ErrNotFound)
	})

	// behavior should mimic the existing exec.LookPath function for binaries on
	// the container.
	t.Run("exec.LookPath", func(t *testing.T) {
		tempRoot := t.TempDir()
		t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+tempRoot)

		assert.NoError(t, os.WriteFile(filepath.Join(tempRoot, "foo"), []byte("echo foo"), 0755))

		shCmd := osext.NewExec("").Command("sh")
		shPath, err := exec.LookPath("sh")

		assert.NoError(t, err)
		assert.Equal(t, shCmd.Path, shPath)
	})

	// absolute paths on the host root need to be resolved correctly, and
	// notably without their host root prefix (eg. /host) because the actual
	// call will already be made with a chroot attribute.
	t.Run("AbsolutePathOnHost", func(t *testing.T) {
		tempRoot := t.TempDir()
		bin := filepath.Join(tempRoot, "bin")

		assert.NoError(t, os.MkdirAll(bin, 0755))

		t.Run("WithRoot", func(t *testing.T) {
			fooSh := osext.NewExec(tempRoot).Command(filepath.Join(tempRoot, "/bin/foo.sh"))

			assert.NoError(t, fooSh.Err)
			assert.Equal(t, "/bin/foo.sh", fooSh.Path)
		})

		t.Run("WithoutRoot", func(t *testing.T) {
			fooSh := osext.NewExec(tempRoot).Command("/bin/foo.sh")

			assert.NoError(t, fooSh.Err)
			assert.Equal(t, "/bin/foo.sh", fooSh.Path)
		})
	})

	// this test case is inspired by how the aws cli installed and
	// discovered on the Amazon Linux 2023 EKS AMIs.
	//
	// > realpath aws
	// /usr/local/aws-cli/v2/2.27.32/dist/aws
	//
	// > which aws
	// /usr/bin/aws
	// > ls -l /usr/bin/aws
	// /usr/bin/aws -> /usr/local/aws-cli/v2/current/bin/aws
	// > ls -l /usr/local/aws-cli/v2/current
	// /usr/local/aws-cli/v2/current -> /usr/local/aws-cli/v2/2.27.32
	// > ls -l /usr/local/aws-cli/v2/2.27.32
	// /usr/local/aws-cli/v2/2.27.32/bin/aws -> ../dist/aws
	t.Run("SymlinksOnHost", func(t *testing.T) {
		tempRoot := t.TempDir()
		// anytime a symlink is written we make it believe that the temporary
		// root is the real host, so the symlink appears broken to the agent and
		// we have to apply our special heuristics.
		trimRoot := func(a string) string { return strings.TrimPrefix(a, tempRoot) }
		bin := filepath.Join(tempRoot, "bin")
		usrBin := filepath.Join(tempRoot, "usr/bin")

		// create:	/X/usr/bin
		assert.NoError(t, os.MkdirAll(usrBin, 0755))
		// create:	/X/bin -> /usr/bin
		assert.NoError(t, os.Symlink(trimRoot(usrBin), bin))

		fooSh := filepath.Join(usrBin, "foo.sh")
		foo := filepath.Join(tempRoot, "foo")
		foo1 := filepath.Join(foo, "1")
		fooCur := filepath.Join(foo, "cur")
		foo1BazSh := filepath.Join(foo1, "baz.sh")
		fooCurBazSh := filepath.Join(fooCur, "baz.sh")

		// create:	/X/foo/1/
		assert.NoError(t, os.MkdirAll(foo1, 0755))
		// create:	/X/foo/cur/ -> /foo/1/
		assert.NoError(t, os.Symlink(trimRoot(foo1), fooCur))
		// create:	/X/foo/1/baz.sh
		assert.NoError(t, os.WriteFile(foo1BazSh, []byte("echo baz"), 0755))
		// create:	/usr/bin/foo.sh -> /foo/cur/baz.sh
		assert.NoError(t, os.Symlink(trimRoot(fooCurBazSh), fooSh))

		bazShCmd := osext.NewExec(tempRoot).Command("foo.sh")

		assert.NoError(t, bazShCmd.Err)
		assert.Equal(t, "/foo/1/baz.sh", bazShCmd.Path)
	})
}
