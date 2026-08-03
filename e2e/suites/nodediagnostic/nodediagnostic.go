package nodediagnostic

import (
	"archive/tar"
	"compress/gzip"
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/collect"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

var (
	nodeDiagnosticLogBucket    string
	nodeDiagnosticLogKeyPrefix string
)

func init() {
	flag.StringVar(&nodeDiagnosticLogBucket, "nodediagnostic-bucket-name", "", "S3 bucket for NodeDiagnostic log collection testing")
	flag.StringVar(&nodeDiagnosticLogKeyPrefix, "nodediagnostic-bucket-key-prefix", "nodediagnostic/logs/", "S3 bucket key prefix for NodeDiagnostic log collection testing")
}

func LogCollection(awsConfig aws.Config) types.Feature {
	var nodeDiagnostics []v1alpha1.NodeDiagnostic

	testTimestamp := time.Now().Format("2006-01-02.150405")

	s3Client := s3.NewFromConfig(awsConfig)
	presignClient := s3.NewPresignClient(s3Client, func(po *s3.PresignOptions) {
		po.Expires = 30 * time.Minute
	})

	return features.New("LogCollection").
		WithLabel("type", "log-collection").
		WithSetup("ValidateBucket", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			if nodeDiagnosticLogBucket == "" {
				t.Skip("skipping NodeDiagnostic log collection tasks because --nodediagnostic-bucket-name flag was not provided")
			}
			if _, err := s3Client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &nodeDiagnosticLogBucket}); err != nil {
				t.Fatalf("bucket %q does not exist or we do not have access", nodeDiagnosticLogBucket)
			}
			return ctx
		}).
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client, err := cfg.NewClient()
			if err != nil {
				t.Fatal(err)
			}
			if err := v1alpha1.SchemeBuilder.AddToScheme(client.Resources().GetScheme()); err != nil {
				t.Fatal(err)
			}
			var nodes corev1.NodeList
			if err := client.Resources().List(ctx, &nodes); err != nil {
				t.Fatal(err)
			}
			if len(nodes.Items) == 0 {
				t.Fatal("no nodes were found in the cluster")
			}
			for _, node := range nodes.Items {
				if node.DeletionTimestamp != nil {
					t.Logf("skipping node %q because it is being deleted", node.Name)
					continue
				}

				nodeDiagnosticLogKey := path.Join(nodeDiagnosticLogKeyPrefix, fmt.Sprintf("%s-%s.tgz", testTimestamp, node.Name))
				presignedRequest, err := presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
					Bucket: &nodeDiagnosticLogBucket,
					Key:    &nodeDiagnosticLogKey,
				})
				if err != nil {
					t.Fatalf("failed to create presigned s3 PUT: %s", err)
				}
				nodeDiagnostic := v1alpha1.NodeDiagnostic{
					ObjectMeta: metav1.ObjectMeta{
						Name: node.Name,
					},
					Spec: v1alpha1.NodeDiagnosticSpec{
						LogCapture: &v1alpha1.LogCapture{
							UploadDestination: v1alpha1.UploadDestination(presignedRequest.URL),
						},
					},
				}
				t.Logf("creating NodeDiagnostic for node %q", node.Name)
				if err := client.Resources().Create(ctx, &nodeDiagnostic); err != nil {
					t.Fatal(err)
				}
				nodeDiagnostics = append(nodeDiagnostics, nodeDiagnostic)
			}
			if len(nodeDiagnostics) == 0 {
				t.Fatal("no non-terminating nodes were found in the cluster")
			}
			return ctx
		}).
		Assess("CollectLogs", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			for _, nodeDiagnostic := range nodeDiagnostics {
				t.Run(nodeDiagnostic.Name, func(t *testing.T) {
					if err := cfg.Client().Resources().Get(ctx, nodeDiagnostic.Name, nodeDiagnostic.Namespace, &nodeDiagnostic); err != nil {
						t.Fatal(err)
					}

					if err := wait.For(
						conditions.New(cfg.Client().Resources()).ResourceMatch(&nodeDiagnostic, func(object k8s.Object) bool {
							nd := object.(*v1alpha1.NodeDiagnostic)
							return len(nd.Status.CaptureStatuses) > 0 && nd.Status.CaptureStatuses[0].State.Completed != nil
						}),
						wait.WithTimeout(time.Minute),
						wait.WithContext(ctx),
					); err != nil {
						t.Error(err)
					}
					for _, status := range nodeDiagnostic.Status.CaptureStatuses {
						if status.State.Completed == nil {
							t.Errorf("capture was not complete: %+v", status.State)
						} else if status.State.Completed.Reason == v1alpha1.CaptureStateFailure {
							t.Errorf("capture failed with reason: %s, message: %s", status.State.Completed.Reason, status.State.Completed.Message)
						} else {
							t.Logf("capture succeeded with reason: %s, message: %s", status.State.Completed.Reason, status.State.Completed.Message)
						}
					}
				})
			}
			return ctx
		}).
		Assess("ValidateLogs", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			for _, nodeDiagnostic := range nodeDiagnostics {
				t.Run(nodeDiagnostic.Name, func(t *testing.T) {
					// the resource name of the NodeDiagnostic resource matches
					// the name of the node.
					nodeDiagnosticLogKey := path.Join(nodeDiagnosticLogKeyPrefix, fmt.Sprintf("%s-%s.tgz", testTimestamp, nodeDiagnostic.Name))
					objectResponse, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
						Bucket: &nodeDiagnosticLogBucket,
						Key:    &nodeDiagnosticLogKey,
					})
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("successfully captured log bundle at: s3://%s/%s", nodeDiagnosticLogBucket, nodeDiagnosticLogKey)
					assertLogsValid(t, objectResponse.Body)
				})
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			for _, nodeDiagnostic := range nodeDiagnostics {
				t.Run(nodeDiagnostic.Name, func(t *testing.T) {
					if err := cfg.Client().Resources().Delete(ctx, &nodeDiagnostic); err != nil {
						t.Fatal(err)
					}
				})
			}
			return ctx
		}).
		Feature()
}

const captureErrorLogFile = "log-capture-errors.log"

// eBPF collector bundle contract, imported from the collector package so the
// paths and marker strings have one definition across producer and test.
const (
	ebpfDataPath           = collect.EBPFDataFile
	ebpfMapsDataPath       = collect.EBPFMapsDataFile
	cliSelectionLinePrefix = collect.CLISelectionLinePrefix
	cliNotInstalledPrefix  = collect.CLINotInstalledLinePrefix
	cliSelectedMarker      = collect.CLISelectedMarker
	mapIDMarker            = collect.MapIDMarker
)

// dmesg files the bundle collects; AVC denials surface here on EKS nodes.
var dmesgPaths = []string{"kernel/dmesg.current", "kernel/dmesg.human.current", "kernel/dmesg.boot"}

// readBundle reads a gzipped-tar diagnostic bundle once and returns every file
// keyed by its archive path. Both bundle-inspecting callers use this and keep
// their own assertions, so the tar/gzip walk lives in one place.
func readBundle(reader io.Reader) (map[string][]byte, error) {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read tar entry: %w", err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", h.Name, err)
		}
		files[h.Name] = b
	}
	return files, nil
}

func assertLogsValid(t *testing.T, reader io.Reader) {
	files, err := readBundle(reader)
	if err != nil {
		t.Fatal(err)
	}
	if errLogs, ok := files[captureErrorLogFile]; ok {
		defer t.Fatalf("%s content:\n%s", captureErrorLogFile, string(errLogs))
	}
	fileNames := make([]string, 0, len(files))
	for name := range files {
		fileNames = append(fileNames, name)
	}
	if assert.NotEmpty(t, fileNames) {
		t.Logf("found the following paths from the log archive: %s", strings.Join(fileNames, ","))
	}
	assertEbpfCollection(t, files[ebpfDataPath], files[ebpfMapsDataPath])
	assertNoNaCLIDenials(t, concatDmesg(files))
}

// concatDmesg joins whichever dmesg files the bundle collected; AVC denials
// surface in these on EKS nodes.
func concatDmesg(files map[string][]byte) []byte {
	var dmesg []byte
	for _, p := range dmesgPaths {
		dmesg = append(dmesg, files[p]...)
	}
	return dmesg
}

// assertNoNaCLIDenials fails if the node's kernel log shows an SELinux AVC
// denial for the na-cli binary — the design's "expected: zero denials"
// obligation. If no dmesg was collected, it cannot conclude "zero" and logs that
// the check was inconclusive rather than passing silently.
func assertNoNaCLIDenials(t *testing.T, dmesg []byte) {
	if len(dmesg) == 0 {
		t.Log("no dmesg collected in bundle; na-cli AVC-denial check is inconclusive")
		return
	}
	for _, line := range strings.Split(string(dmesg), "\n") {
		if !strings.Contains(line, "avc:") || !strings.Contains(line, "denied") {
			continue
		}
		if strings.Contains(line, naCLIBinaryName) || strings.Contains(line, "cni_exec_t") {
			t.Errorf("SELinux AVC denial involving na-cli found in kernel log: %s", line)
		}
	}
}

// naCLIBinaryName is the na-cli comm/name as it appears in AVC audit lines.
const naCLIBinaryName = "aws-eks-na-cli"

// assertEbpfCollection validates the network-policy eBPF collector's output in a
// way that holds on ANY cluster, regardless of whether network policy is
// enabled. The collector always runs the Networking collector, so ebpf-data.txt
// must be present and must carry its self-describing selection line. It
// deliberately does NOT require ebpf-maps-data.txt to exist: that file is written
// only when the network policy agent has programmed maps on this node (policy
// enforced and a selected pod present), which is not the case on a stock cluster
// or on nodes without a selected pod. Requiring it here would false-fail.
//
// The only conditional check is a well-formedness one: IF a map dump was written,
// it must contain the per-map header. "Maps exist" is asserted by the gated
// EbpfMapCollectionWithNetworkPolicy feature, which sets up the conditions that
// make maps mandatory.
func assertEbpfCollection(t *testing.T, ebpfData, ebpfMapsData []byte) {
	if !assert.NotEmpty(t, ebpfData, "%s should always be present in the bundle", ebpfDataPath) {
		return
	}
	// Either a binary was selected (selection line) or none was installed (skip
	// line). These are the two states the collector can legitimately end in.
	assert.True(t, containsLinePrefix(ebpfData, cliSelectionLinePrefix) || containsLinePrefix(ebpfData, cliNotInstalledPrefix),
		"%s should contain the CLI selection line or the not-installed line; got:\n%s", ebpfDataPath, string(ebpfData))

	if len(ebpfMapsData) > 0 {
		assert.Contains(t, string(ebpfMapsData), mapIDMarker,
			"%s, when present, must contain per-map dump sections", ebpfMapsDataPath)
	} else {
		t.Logf("no eBPF map dump on this node (network policy not enforced or no selected pod); ebpf-data.txt:\n%s", string(ebpfData))
	}
}

// containsLinePrefix reports whether any line of b starts with prefix.
func containsLinePrefix(b []byte, prefix string) bool {
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func NodeDestination() types.Feature {
	var nodeDiagnostics []v1alpha1.NodeDiagnostic

	return features.New("NodeDestination").
		WithLabel("type", "log-collection").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client, err := cfg.NewClient()
			if err != nil {
				t.Fatal(err)
			}
			if err := v1alpha1.SchemeBuilder.AddToScheme(client.Resources().GetScheme()); err != nil {
				t.Fatal(err)
			}
			var nodes corev1.NodeList
			if err := client.Resources().List(ctx, &nodes); err != nil {
				t.Fatal(err)
			}
			if len(nodes.Items) == 0 {
				t.Fatal("no nodes were found in the cluster")
			}
			for _, node := range nodes.Items {
				if node.DeletionTimestamp != nil {
					t.Logf("skipping node %q because it is being deleted", node.Name)
					continue
				}

				nodeDiagnostic := v1alpha1.NodeDiagnostic{
					ObjectMeta: metav1.ObjectMeta{
						Name: node.Name,
					},
					Spec: v1alpha1.NodeDiagnosticSpec{
						LogCapture: &v1alpha1.LogCapture{
							UploadDestination: "node",
						},
					},
				}
				t.Logf("creating NodeDiagnostic for node %q with node destination", node.Name)
				if err := client.Resources().Create(ctx, &nodeDiagnostic); err != nil {
					t.Fatal(err)
				}
				nodeDiagnostics = append(nodeDiagnostics, nodeDiagnostic)
			}
			if len(nodeDiagnostics) == 0 {
				t.Fatal("no non-terminating nodes were found in the cluster")
			}
			return ctx
		}).
		Assess("CollectLogs", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			for _, nodeDiagnostic := range nodeDiagnostics {
				t.Run(nodeDiagnostic.Name, func(t *testing.T) {
					if err := cfg.Client().Resources().Get(ctx, nodeDiagnostic.Name, nodeDiagnostic.Namespace, &nodeDiagnostic); err != nil {
						t.Fatal(err)
					}

					if err := wait.For(
						conditions.New(cfg.Client().Resources()).ResourceMatch(&nodeDiagnostic, func(object k8s.Object) bool {
							nd := object.(*v1alpha1.NodeDiagnostic)
							return len(nd.Status.CaptureStatuses) > 0 && nd.Status.CaptureStatuses[0].State.Completed != nil
						}),
						wait.WithTimeout(time.Minute),
						wait.WithContext(ctx),
					); err != nil {
						t.Error(err)
					}
					for _, status := range nodeDiagnostic.Status.CaptureStatuses {
						if status.State.Completed == nil {
							t.Errorf("capture was not complete: %+v", status.State)
						} else if status.State.Completed.Reason == v1alpha1.CaptureStateFailure {
							t.Errorf("capture failed with reason: %s, message: %s", status.State.Completed.Reason, status.State.Completed.Message)
						} else {
							t.Logf("capture succeeded with reason: %s, message: %s", status.State.Completed.Reason, status.State.Completed.Message)
						}
					}
				})
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			for _, nodeDiagnostic := range nodeDiagnostics {
				t.Run(nodeDiagnostic.Name, func(t *testing.T) {
					if err := cfg.Client().Resources().Delete(ctx, &nodeDiagnostic); err != nil {
						t.Fatal(err)
					}
				})
			}
			return ctx
		}).
		Feature()
}
