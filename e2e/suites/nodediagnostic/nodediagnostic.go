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
	"github.com/stretchr/testify/assert"
	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
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

func assertLogsValid(t *testing.T, reader io.Reader) {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var fileNames []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if h.Name == captureErrorLogFile {
			errLogs, err := io.ReadAll(tr)
			assert.NoError(t, err)
			defer t.Fatalf("%s content:\n%s", captureErrorLogFile, string(errLogs))
		}
		if err != nil {
			t.Fatalf("failed to read tar entry: %s", err)
		}
		fileNames = append(fileNames, h.Name)
	}
	if assert.NotEmpty(t, fileNames) {
		t.Logf("found the following paths from the log archive: %s", strings.Join(fileNames, ","))
	}
}
