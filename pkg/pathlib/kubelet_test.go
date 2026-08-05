package pathlib

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
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

func TestResolveClusterCACert(t *testing.T) {
	t.Run("embedded CA data needs no path", func(t *testing.T) {
		caCertPath, err := ResolveClusterCACert(t.TempDir(), &clientcmdapi.Cluster{
			CertificateAuthorityData: []byte("---embedded ca---"),
		})
		assert.NoError(t, err)
		assert.Empty(t, caCertPath)
	})

	t.Run("referenced CA file is prefixed with the host root", func(t *testing.T) {
		root := t.TempDir()
		caCertPath, err := ResolveClusterCACert(root, &clientcmdapi.Cluster{
			CertificateAuthority: DefaultCACertPath,
		})
		assert.NoError(t, err)
		assert.Equal(t, filepath.Join(root, DefaultCACertPath), caCertPath)
	})

	t.Run("referenced CA file already inside the host root is left as-is", func(t *testing.T) {
		root := t.TempDir()
		want := filepath.Join(root, DefaultCACertPath)
		caCertPath, err := ResolveClusterCACert(root, &clientcmdapi.Cluster{CertificateAuthority: want})
		assert.NoError(t, err)
		assert.Equal(t, want, caCertPath)
	})

	t.Run("falls back to the well-known host location", func(t *testing.T) {
		root := t.TempDir()
		SetupFile(t, root, DefaultCACertPath)
		caCertPath, err := ResolveClusterCACert(root, &clientcmdapi.Cluster{})
		assert.NoError(t, err)
		assert.Equal(t, filepath.Join(root, DefaultCACertPath), caCertPath)
	})

	t.Run("errors when no CA can be located", func(t *testing.T) {
		_, err := ResolveClusterCACert(t.TempDir(), &clientcmdapi.Cluster{})
		assert.Error(t, err)
	})

	t.Run("errors for a nil cluster without a well-known CA", func(t *testing.T) {
		_, err := ResolveClusterCACert(t.TempDir(), nil)
		assert.Error(t, err)
	})
}

func TestClusterForCurrentContext(t *testing.T) {
	t.Run("resolves the cluster referenced by the current context", func(t *testing.T) {
		want := &clientcmdapi.Cluster{Server: "https://current"}
		assert.Same(t, want, ClusterForCurrentContext(&clientcmdapi.Config{
			CurrentContext: "ctx",
			Contexts:       map[string]*clientcmdapi.Context{"ctx": {Cluster: "wanted"}},
			Clusters: map[string]*clientcmdapi.Cluster{
				"wanted": want,
				"other":  {Server: "https://other"},
			},
		}))
	})

	t.Run("falls back to the sole cluster when the context is missing", func(t *testing.T) {
		want := &clientcmdapi.Cluster{Server: "https://only"}
		assert.Same(t, want, ClusterForCurrentContext(&clientcmdapi.Config{
			Clusters: map[string]*clientcmdapi.Cluster{"only": want},
		}))
	})

	t.Run("returns nil when the context is missing and clusters are ambiguous", func(t *testing.T) {
		assert.Nil(t, ClusterForCurrentContext(&clientcmdapi.Config{
			Clusters: map[string]*clientcmdapi.Cluster{
				"a": {Server: "https://a"},
				"b": {Server: "https://b"},
			},
		}))
	})
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
