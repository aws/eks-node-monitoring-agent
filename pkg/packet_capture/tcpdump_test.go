package packet_capture

import (
	"testing"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/stretchr/testify/assert"
)

func TestBuildTcpdumpArgs_DefaultChunkSize(t *testing.T) {
	spec := &v1alpha1.PacketCapture{
		Mode:        v1alpha1.ModeTcpdump,
		Duration:    "30s",
		ChunkSizeMB: 0,
		Upload: v1alpha1.PacketCaptureUpload{
			URL:    "https://example.com",
			Fields: map[string]string{},
		},
	}
	args := buildTcpdumpArgs("/tmp/capture.pcap", spec)
	assertFlagValue(t, args, "-C", "10")
}

func TestBuildTcpdumpArgs_CustomChunkSize(t *testing.T) {
	spec := &v1alpha1.PacketCapture{
		Mode:        v1alpha1.ModeTcpdump,
		Duration:    "30s",
		ChunkSizeMB: 50,
		Upload: v1alpha1.PacketCaptureUpload{
			URL:    "https://example.com",
			Fields: map[string]string{},
		},
	}
	args := buildTcpdumpArgs("/tmp/capture.pcap", spec)
	assertFlagValue(t, args, "-C", "50")
}

func TestBuildTcpdumpArgs_WithInterfaces(t *testing.T) {
	spec := &v1alpha1.PacketCapture{
		Mode:      v1alpha1.ModeTcpdump,
		Duration:  "30s",
		Interface: "eth0",
		Upload: v1alpha1.PacketCaptureUpload{
			URL:    "https://example.com",
			Fields: map[string]string{},
		},
	}
	args := buildTcpdumpArgs("/tmp/capture.pcap", spec)
	assertFlagValue(t, args, "-i", "eth0")
}

func TestBuildTcpdumpArgs_WithoutInterfaces(t *testing.T) {
	spec := &v1alpha1.PacketCapture{
		Mode:     v1alpha1.ModeTcpdump,
		Duration: "30s",
		Upload: v1alpha1.PacketCaptureUpload{
			URL:    "https://example.com",
			Fields: map[string]string{},
		},
	}
	args := buildTcpdumpArgs("/tmp/capture.pcap", spec)
	// When no interface specified, -i flag should not be present (tcpdump uses its default)
	for _, arg := range args {
		if arg == "-i" {
			t.Fatal("-i flag should not be present when interface is empty")
		}
	}
}

func TestBuildTcpdumpArgs_WithFilter(t *testing.T) {
	spec := &v1alpha1.PacketCapture{
		Mode:     v1alpha1.ModeTcpdump,
		Duration: "30s",
		Filter:   "port 80",
		Upload: v1alpha1.PacketCaptureUpload{
			URL:    "https://example.com",
			Fields: map[string]string{},
		},
	}
	args := buildTcpdumpArgs("/tmp/capture.pcap", spec)
	assert.Equal(t, "port 80", args[len(args)-1], "filter should be the last argument")
}

func TestBuildTcpdumpArgs_WithoutFilter(t *testing.T) {
	spec := &v1alpha1.PacketCapture{
		Mode:     v1alpha1.ModeTcpdump,
		Duration: "30s",
		Upload: v1alpha1.PacketCaptureUpload{
			URL:    "https://example.com",
			Fields: map[string]string{},
		},
	}
	args := buildTcpdumpArgs("/tmp/capture.pcap", spec)
	// Last arg should not be a filter — it should be a known flag value
	lastArg := args[len(args)-1]
	assert.NotEqual(t, "", lastArg)
	// Verify no standalone filter-like string at the end
	assert.Contains(t, []string{"igzip", "10000", "-U", "any"}, lastArg,
		"last arg should be a known flag value when no filter is set, or an interface/chunk value")
}

// assertFlagValue checks that args contains flag followed by expectedValue.
func assertFlagValue(t *testing.T, args []string, flag, expectedValue string) {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			assert.Equal(t, expectedValue, args[i+1], "flag %s value mismatch", flag)
			return
		}
	}
	t.Fatalf("flag %s not found in args: %v", flag, args)
}
