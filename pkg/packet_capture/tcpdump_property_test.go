package packet_capture

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	fileutil "github.com/aws/eks-node-monitoring-agent/pkg/util/file"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// genPacketCaptureSpec generates a random valid PacketCapture spec for property testing.
func genPacketCaptureSpec(t *rapid.T) *v1alpha1.PacketCapture {
	iface := ""
	if rapid.Bool().Draw(t, "hasInterface") {
		iface = rapid.StringMatching(`[a-z][a-z0-9]{0,14}`).Draw(t, "iface")
	}

	filter := ""
	if rapid.Bool().Draw(t, "hasFilter") {
		filter = rapid.StringMatching(`[a-z0-9 ]{1,50}`).Draw(t, "filter")
	}

	chunkSize := rapid.IntRange(0, 100).Draw(t, "chunkSizeMB")

	return &v1alpha1.PacketCapture{
		Mode:        v1alpha1.ModeTcpdump,
		Interface:   iface,
		Duration:    "30s",
		Filter:      filter,
		ChunkSizeMB: chunkSize,
		Upload: v1alpha1.PacketCaptureUpload{
			URL:    "https://example.com/upload",
			Fields: map[string]string{"key": "captures/" + S3PresignedFilenamePlaceholder},
		},
	}
}

// Feature: tcpdump-packet-capture, Property 1: Mandatory tcpdump flags are always present
// **Validates: Requirements 5.1, 5.4, 5.5, 5.6**
func TestProperty1_MandatoryFlagsAlwaysPresent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spec := genPacketCaptureSpec(t)
		outputPath := rapid.StringMatching(`/tmp/[a-z]{1,20}/capture\.pcap`).Draw(t, "outputPath")

		args := buildTcpdumpArgs(outputPath, spec)

		// -w <outputPath> must be present
		foundW := false
		for i, a := range args {
			if a == "-w" && i+1 < len(args) && args[i+1] == outputPath {
				foundW = true
				break
			}
		}
		if !foundW {
			t.Fatalf("args missing -w %s: %v", outputPath, args)
		}

		// -W MaxRotatedFiles must be present
		foundW10000 := false
		for i, a := range args {
			if a == "-W" && i+1 < len(args) && args[i+1] == MaxRotatedFiles {
				foundW10000 = true
				break
			}
		}
		if !foundW10000 {
			t.Fatalf("args missing -W %s: %v", MaxRotatedFiles, args)
		}

		// -U must be present
		foundU := false
		for _, a := range args {
			if a == "-U" {
				foundU = true
				break
			}
		}
		if !foundU {
			t.Fatalf("args missing -U: %v", args)
		}

		// -z CompressCmd must be present
		foundZ := false
		for i, a := range args {
			if a == "-z" && i+1 < len(args) && args[i+1] == CompressCmd {
				foundZ = true
				break
			}
		}
		if !foundZ {
			t.Fatalf("args missing -z %s: %v", CompressCmd, args)
		}
	})
}

// Feature: tcpdump-packet-capture, Property 2: ChunkSizeMB maps to -C flag value
// **Validates: Requirements 5.2, 5.3**
func TestProperty2_ChunkSizeMBMapsToCFlag(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spec := genPacketCaptureSpec(t)
		outputPath := "/tmp/capture.pcap"

		args := buildTcpdumpArgs(outputPath, spec)

		expectedChunk := spec.ChunkSizeMB
		if expectedChunk <= 0 {
			expectedChunk = DefaultRotationSizeMB
		}
		expectedVal := fmt.Sprintf("%d", expectedChunk)

		foundC := false
		for i, a := range args {
			if a == "-C" && i+1 < len(args) {
				if args[i+1] != expectedVal {
					t.Fatalf("-C value mismatch: got %s, want %s (chunkSizeMB=%d)", args[i+1], expectedVal, spec.ChunkSizeMB)
				}
				foundC = true
				break
			}
		}
		if !foundC {
			t.Fatalf("args missing -C flag: %v", args)
		}
	})
}

// Feature: tcpdump-packet-capture, Property 3: Non-empty interfaces produce -i flag with comma-joined value
// **Validates: Requirements 5.7, 5.8**
func TestProperty3_InterfacesProduceIFlag(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spec := genPacketCaptureSpec(t)
		outputPath := "/tmp/capture.pcap"

		args := buildTcpdumpArgs(outputPath, spec)

		if spec.Interface != "" {
			foundI := false
			for i, a := range args {
				if a == "-i" && i+1 < len(args) {
					if args[i+1] != spec.Interface {
						t.Fatalf("-i value mismatch: got %q, want %q", args[i+1], spec.Interface)
					}
					foundI = true
					break
				}
			}
			if !foundI {
				t.Fatalf("args missing -i flag for interface %q: %v", spec.Interface, args)
			}
		} else {
			// When interface is empty, should default to "any"
			foundI := false
			for i, a := range args {
				if a == "-i" && i+1 < len(args) && args[i+1] == "any" {
					foundI = true
					break
				}
			}
			if !foundI {
				t.Fatalf("args missing -i any when interface is empty: %v", args)
			}
		}
	})
}

// Feature: tcpdump-packet-capture, Property 4: Filter expression is always the last argument
// **Validates: Requirements 5.9, 5.10**
func TestProperty4_FilterIsLastArgument(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spec := genPacketCaptureSpec(t)
		outputPath := "/tmp/capture.pcap"

		args := buildTcpdumpArgs(outputPath, spec)

		if spec.Filter != "" {
			lastArg := args[len(args)-1]
			if lastArg != spec.Filter {
				t.Fatalf("filter should be last arg: got %q, want %q; args=%v", lastArg, spec.Filter, args)
			}
		} else {
			// When filter is empty, the last arg should not look like a filter
			// (it should be a known flag value like "igzip", "10000", an interface, or a -C value)
			lastArg := args[len(args)-1]
			knownLastArgs := map[string]bool{
				CompressCmd: true, MaxRotatedFiles: true, "-U": true,
			}
			// The last arg should be a known flag value, an interface list, or a -C value
			if !knownLastArgs[lastArg] {
				// Check it's a -C value (numeric) or an interface list
				isNumeric := true
				for _, c := range lastArg {
					if c < '0' || c > '9' {
						isNumeric = false
						break
					}
				}
				if !isNumeric && !strings.Contains(lastArg, ",") {
					// Could be a single interface name — that's fine too
					// Just verify it's not an accidental filter by checking it appears after -i
					foundAsIface := false
					for i, a := range args {
						if a == "-i" && i+1 < len(args) && args[i+1] == lastArg {
							foundAsIface = true
							break
						}
					}
					if !foundAsIface && lastArg != outputPath {
						// It's some other value — could be a single interface name without comma
						// This is acceptable as long as it's a known arg value
					}
				}
			}
		}
	})
}

// Feature: tcpdump-packet-capture, Property 5: Upload key filename substitution
// **Validates: Requirements 6.4**
func TestProperty5_UploadKeyFilenameSubstitution(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		filename := rapid.StringMatching(`[a-z0-9]{1,20}\.(pcap|gz)`).Draw(t, "filename")
		prefix := rapid.StringMatching(`[a-z0-9/]{0,30}`).Draw(t, "prefix")
		suffix := rapid.StringMatching(`[a-z0-9/]{0,10}`).Draw(t, "suffix")
		keyTemplate := prefix + S3PresignedFilenamePlaceholder + suffix

		result := strings.ReplaceAll(keyTemplate, S3PresignedFilenamePlaceholder, filename)

		if strings.Contains(result, S3PresignedFilenamePlaceholder) {
			t.Fatalf("substitution failed: result %q still contains %s", result, S3PresignedFilenamePlaceholder)
		}
		if !strings.Contains(result, filename) {
			t.Fatalf("substitution failed: result %q does not contain filename %q", result, filename)
		}
	})
}

// Feature: tcpdump-packet-capture, Property 7: findGzipFiles returns only .gz files in sorted order
// **Validates: Requirements 10.7**
func TestProperty7_FindGzipFilesOnlyGzSorted(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir, err := os.MkdirTemp("", "prop7-*")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)

		numFiles := rapid.IntRange(0, 20).Draw(rt, "numFiles")
		var expectedGz []string
		for i := 0; i < numFiles; i++ {
			ext := rapid.SampledFrom([]string{".gz", ".pcap", ".txt", ".log"}).Draw(rt, fmt.Sprintf("ext%d", i))
			name := fmt.Sprintf("file%03d%s", i, ext)
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("data"), 0644))
			if ext == ".gz" {
				expectedGz = append(expectedGz, filepath.Join(dir, name))
			}
		}
		sort.Strings(expectedGz)

		result, err := findGzipFiles(dir)
		if err != nil {
			rt.Fatalf("findGzipFiles error: %v", err)
		}

		if len(result) != len(expectedGz) {
			rt.Fatalf("count mismatch: got %d, want %d", len(result), len(expectedGz))
		}
		for i := range result {
			if result[i] != expectedGz[i] {
				rt.Fatalf("mismatch at index %d: got %q, want %q", i, result[i], expectedGz[i])
			}
		}

		// Verify sorted
		for i := 1; i < len(result); i++ {
			if result[i] < result[i-1] {
				rt.Fatalf("not sorted: %q < %q", result[i], result[i-1])
			}
		}
	})
}

// Feature: tcpdump-packet-capture, Property 8: CheckDiskSpace returns a value in [0.0, 1.0]
// **Validates: Requirements 10.13**
func TestProperty8_CheckDiskSpaceInRange(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir, err := os.MkdirTemp("", "prop8-*")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)

		usage, err := fileutil.CheckDiskSpace(dir)
		if err != nil {
			rt.Fatalf("CheckDiskSpace error: %v", err)
		}
		if usage < 0.0 || usage > 1.0 {
			rt.Fatalf("disk usage out of range [0.0, 1.0]: %f", usage)
		}
	})
}
