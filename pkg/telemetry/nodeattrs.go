package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nmaconfig "github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
)

// How often WaitForNodeAttrs retries different data sources
const (
	// imds is cheap and local
	imdsPollInterval = time.Second
	// remote API server call
	nodePollInterval = 30 * time.Second
)

type imdsClient interface {
	GetMetadata(ctx context.Context, params *imds.GetMetadataInput, optFns ...func(*imds.Options)) (*imds.GetMetadataOutput, error)
}

// WaitForNodeAttrs returns the report's resource attributes for the named node. It retries until both are
// available, and only returns an error once ctx is done.
func WaitForNodeAttrs(ctx context.Context, logger logr.Logger, kubeClient client.Reader, nodeName string) (nodetelemetry.NodeAttrs, error) {
	return waitForNodeAttrs(ctx, logger, newIMDSClient, kubeClient, nodeName, nmaconfig.GetRuntimeContext().Tags(), imdsPollInterval, nodePollInterval)
}

func newIMDSClient(ctx context.Context, logger logr.Logger) imdsClient {
	// try to load the default config in case env vars like AWS_EC2_METADATA_SERVICE_ENDPOINT are set
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		logger.Error(err, "using the default IMDS client settings")
		return imds.New(imds.Options{})
	}
	return imds.NewFromConfig(awsCfg)
}

func waitForNodeAttrs(ctx context.Context, logger logr.Logger, newIMDS func(context.Context, logr.Logger) imdsClient, kubeClient client.Reader, nodeName string, tags []string, imdsInterval, nodeInterval time.Duration) (nodetelemetry.NodeAttrs, error) {
	attrs := nodetelemetry.NodeAttrs{NodeName: nodeName, HostArch: runtime.GOARCH}
	addComputeType(&attrs, tags)
	if attrs.ComputeType != nmaconfig.Hybrid {
		// try to load the default config in case env vars like AWS_EC2_METADATA_SERVICE_ENDPOINT are set
		// TODO: determine additional attributes for hybrid nodes
		imdsClient := newIMDS(ctx, logger)
		if err := wait.PollUntilContextCancel(ctx, imdsInterval, true, func(ctx context.Context) (bool, error) {
			err := addIMDSAttrs(ctx, imdsClient, &attrs)
			if err != nil {
				logger.V(4).Info("waiting for node attributes from IMDS", "error", err.Error())
			}
			return err == nil, nil
		}); err != nil {
			return nodetelemetry.NodeAttrs{}, err
		}
	}
	if err := wait.PollUntilContextCancel(ctx, nodeInterval, true, func(ctx context.Context) (bool, error) {
		// SAFETY: kubelet sets its version in the initial node object it creates
		// https://github.com/kubernetes/kubernetes/blob/f9408fbfca0576dafad7595c98c0abbe66423558/pkg/kubelet/kubelet_node_status.go#L371
		// unlike most k8s objects, nodes are allowed to retain status fields in the initial create:
		// https://github.com/kubernetes/kubernetes/blob/f9408fbfca0576dafad7595c98c0abbe66423558/pkg/registry/core/node/strategy.go#L96-L124
		// TODO: consider combining with the hybrid nodes bootstrapper as a generic runtime context bootstrapper
		return addKubeletVersion(ctx, kubeClient, &attrs) == nil, nil
	}); err != nil {
		return nodetelemetry.NodeAttrs{}, err
	}
	return attrs, nil
}

// addComputeType sets ComputeType from the runtime tags
func addComputeType(attrs *nodetelemetry.NodeAttrs, tags []string) {
	for _, tag := range []string{nmaconfig.EKSAuto, nmaconfig.Hybrid} {
		if slices.Contains(tags, tag) {
			attrs.ComputeType = tag
			return
		}
	}
}

// addIMDSAttrs sets the instance attributes from IMDS.
func addIMDSAttrs(ctx context.Context, client imdsClient, attrs *nodetelemetry.NodeAttrs) error {
	for _, field := range []struct {
		path        string
		destination *string
	}{
		{"instance-id", &attrs.HostID},
		{"instance-type", &attrs.HostType},
		{"ami-id", &attrs.ImageID},
		{"placement/availability-zone-id", &attrs.ZoneID},
	} {
		value, err := getMetadata(ctx, client, field.path)
		if err != nil {
			return err
		}
		*field.destination = value
	}
	return nil
}

func addKubeletVersion(ctx context.Context, kubeClient client.Reader, attrs *nodetelemetry.NodeAttrs) error {
	var node corev1.Node
	if err := kubeClient.Get(ctx, types.NamespacedName{Name: attrs.NodeName}, &node); err != nil {
		return err
	}
	if node.Status.NodeInfo.KubeletVersion == "" {
		return errors.New("node has no kubelet version yet")
	}
	attrs.KubeletVersion = node.Status.NodeInfo.KubeletVersion
	return nil
}

func getMetadata(ctx context.Context, client imdsClient, path string) (string, error) {
	output, err := client.GetMetadata(ctx, &imds.GetMetadataInput{Path: path})
	if err != nil {
		return "", fmt.Errorf("get %s from IMDS: %w", path, err)
	}
	defer output.Content.Close()
	body, err := io.ReadAll(output.Content)
	if err != nil {
		return "", fmt.Errorf("read %s from IMDS: %w", path, err)
	}
	return strings.TrimSpace(string(body)), nil
}
