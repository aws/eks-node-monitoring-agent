package pathlib

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolvePath(t *testing.T) {
	for _, test := range []struct {
		name     string
		file     string
		dir      string
		resolver func(string) string
	}{
		{
			name:     "cacert",
			file:     DefaultCACertPath,
			resolver: ResolveCACertPath,
		},
		{
			name:     "kubeconfig",
			file:     "/var/lib/kubelet/kubeconfig",
			resolver: ResolveKubeconfig,
		},
		{
			name:     "kubelet-config",
			file:     "/etc/kubernetes/kubelet/config.json",
			resolver: ResolveKubeletConfig,
		},
		{
			name:     "kubelet-config-drop-in",
			dir:      "/etc/kubernetes/kubelet/config.json.d",
			resolver: ResolveKubeletConfigDropIn,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.file != "" {
				SetupFile(t, root, test.file)
			}
			if test.dir != "" {
				SetupDir(t, root, test.dir)
			}
			assert.NotEmpty(t, test.resolver(root))
		})
	}

	for _, test := range []struct {
		name     string
		resolver func(string) string
	}{
		{
			name:     "missing-kubelet-config",
			resolver: ResolveKubeletConfig,
		},
		{
			name:     "missing-kubelet-config-drop-in",
			resolver: ResolveKubeletConfigDropIn,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			assert.Empty(t, test.resolver(root))
		})
	}
}

func SetupFile(t *testing.T, root, file string) {
	file = filepath.Join(root, file)
	assert.NoError(t, os.MkdirAll(filepath.Dir(file), 0755))
	assert.NoError(t, os.WriteFile(file, []byte("foo"), 0755))
}

func SetupDir(t *testing.T, root, dir string) {
	dir = filepath.Join(root, dir)
	assert.NoError(t, os.MkdirAll(dir, 0755))
}
