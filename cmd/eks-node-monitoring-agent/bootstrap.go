package main

import (
	"context"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/eks-node-monitoring-agent/pkg/config"
)

type Bootstrapper interface {
	Bootstrap(context.Context) error
}

func NewHybridNodesBootstrapper(kubeClient client.Client, node *corev1.Node) *hybridNodeBootstrapper {
	return &hybridNodeBootstrapper{
		kubeClient: kubeClient,
		node:       node,
	}
}

const (
	hybridProviderPrefix = "eks-hybrid://"
	computeTypeLabelKey  = "eks.amazonaws.com/compute-type"
)

type hybridNodeBootstrapper struct {
	kubeClient client.Client
	node       *corev1.Node
}

func (b *hybridNodeBootstrapper) Bootstrap(ctx context.Context) error {
	logger := log.FromContext(ctx)
	logger.V(4).Info("determining whether host is an EKS Hybrid Node")
	// it's safe to assume this call can be blocking, because we know that the
	// node will be registered with the EKS Hybrid Node compute-type labels via
	// the --node-labels kubelet flag.
	// see: https://github.com/aws/eks-hybrid/blob/b32ecfe4bbf5ba06b35065535d8010918f5d5f10/internal/kubelet/config.go#L279-L281
	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (done bool, err error) {
		if err = b.populateInfo(ctx, b.node); err == nil {
			return true, nil
		}
		// node object not being found is retryable
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		logger.Error(err, "failed to determine whether host is an EKS Hybrid Node")
		return false, err
	})
}

func (b *hybridNodeBootstrapper) populateInfo(ctx context.Context, node *corev1.Node) error {
	var newNode corev1.Node
	if err := b.kubeClient.Get(ctx, client.ObjectKeyFromObject(node), &newNode); err != nil {
		return err
	}
	if b.isHybridNode(&newNode) {
		config.GetRuntimeContext().AddTags(config.Hybrid)
	}
	return nil
}

func (b *hybridNodeBootstrapper) isHybridNode(node *corev1.Node) bool {
	if strings.HasPrefix(node.Spec.ProviderID, hybridProviderPrefix) {
		return true
	}
	if value, exists := node.Labels[computeTypeLabelKey]; exists && value == config.Hybrid {
		return true
	}
	return false
}
