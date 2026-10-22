package main

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry"
	telemetryconfig "github.com/aws/eks-node-monitoring-agent/pkg/telemetry/config"
)

const resourceInstanceID = "instanceId"

func runTelemetry(ctx context.Context, logger logr.Logger, cfg *telemetryconfig.Config, restCfg *rest.Config, kubeClient client.Client, source telemetry.FatalSource) {
	sender, err := nodetelemetry.NewClient(restCfg)
	if err != nil {
		logger.Error(err, "node telemetry disabled")
		return
	}
	var node corev1.Node
	if err := wait.PollUntilContextCancel(ctx, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		return kubeClient.Get(ctx, types.NamespacedName{Name: hostname}, &node) == nil, nil
	}); err != nil {
		return
	}
	attrs := telemetry.NodeAttrsFromNode(&node)
	var resources map[string]string
	if attrs.HostID != "" {
		resources = map[string]string{resourceInstanceID: attrs.HostID}
	}
	collectors, scrapeWait := telemetry.BuildCollectors(cfg, source, resources, logger)

	logger.Info("starting node telemetry", "interval", cfg.ReportInterval(), "collectors", len(collectors))
	_ = telemetry.NewManager(telemetry.ManagerConfig{
		Interval:   cfg.ReportInterval(),
		NodeName:   hostname,
		Node:       attrs,
		Collectors: collectors,
		ScrapeWait: scrapeWait,
		Sender:     sender,
		Registerer: metrics.Registry,
		Logger:     logger,
	}).Start(ctx)
}
