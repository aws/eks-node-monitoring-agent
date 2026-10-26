package telemetry

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nmaconfig "github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
)

type fakeIMDS map[string]string

func (metadata fakeIMDS) GetMetadata(_ context.Context, params *imds.GetMetadataInput, _ ...func(*imds.Options)) (*imds.GetMetadataOutput, error) {
	value, ok := metadata[params.Path]
	if !ok {
		return nil, errors.New("not found")
	}
	return &imds.GetMetadataOutput{Content: io.NopCloser(strings.NewReader(value))}, nil
}

func newFakeIMDS(metadata fakeIMDS) func(context.Context, logr.Logger) imdsClient {
	return func(context.Context, logr.Logger) imdsClient { return metadata }
}

func instanceMetadata() fakeIMDS {
	return fakeIMDS{
		"instance-id":                    "i-0123456789abcdef0",
		"instance-type":                  "m5.large",
		"ami-id":                         "ami-0123456789abcdef0",
		"placement/availability-zone-id": "usw2-az1",
	}
}

func TestAddIMDSAttrs(t *testing.T) {
	metadata := instanceMetadata()
	attrs := nodetelemetry.NodeAttrs{NodeName: "node-a"}
	require.NoError(t, addIMDSAttrs(context.Background(), metadata, &attrs))
	assert.Equal(t, nodetelemetry.NodeAttrs{
		NodeName: "node-a",
		HostID:   "i-0123456789abcdef0",
		HostType: "m5.large",
		ImageID:  "ami-0123456789abcdef0",
		ZoneID:   "usw2-az1",
	}, attrs, "only the IMDS fields are added")

	delete(metadata, "placement/availability-zone-id")
	assert.ErrorContains(t, addIMDSAttrs(context.Background(), metadata, &attrs), "placement/availability-zone-id")
}

func TestWaitForNodeAttrs(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
		Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.33.0"}},
	}
	kubeClient := fake.NewClientBuilder().WithObjects(node).Build()
	attrs, err := waitForNodeAttrs(context.Background(), logr.Discard(), newFakeIMDS(instanceMetadata()), kubeClient, "node-a", []string{nmaconfig.EKSAuto}, time.Millisecond, time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, nodetelemetry.NodeAttrs{
		NodeName:       "node-a",
		HostID:         "i-0123456789abcdef0",
		HostArch:       runtime.GOARCH,
		HostType:       "m5.large",
		ImageID:        "ami-0123456789abcdef0",
		ZoneID:         "usw2-az1",
		KubeletVersion: "v1.33.0",
		ComputeType:    nmaconfig.EKSAuto,
	}, attrs)
}

func TestWaitForNodeAttrs_HybridSkipsIMDS(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
		Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.33.0"}},
	}
	kubeClient := fake.NewClientBuilder().WithObjects(node).Build()
	attrs, err := waitForNodeAttrs(context.Background(), logr.Discard(), func(context.Context, logr.Logger) imdsClient {
		t.Fatal("the IMDS client is created on a hybrid node")
		return nil
	}, kubeClient, "node-a", []string{nmaconfig.Hybrid}, time.Millisecond, time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, nodetelemetry.NodeAttrs{
		NodeName:       "node-a",
		HostArch:       runtime.GOARCH,
		KubeletVersion: "v1.33.0",
		ComputeType:    nmaconfig.Hybrid,
	}, attrs)
}

func TestWaitForNodeAttrs_WaitsForKubeletVersion(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}
	kubeClient := fake.NewClientBuilder().WithObjects(node).Build()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := waitForNodeAttrs(ctx, logr.Discard(), newFakeIMDS(instanceMetadata()), kubeClient, "node-a", nil, time.Millisecond, time.Millisecond)
	assert.Error(t, err, "a Node without a kubelet version isn't used")
}
