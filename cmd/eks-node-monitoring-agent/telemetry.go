package main

import (
	"context"

	"github.com/go-logr/logr"
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
	attrs, err := telemetry.WaitForNodeAttrs(ctx, logger, kubeClient, hostname)
	if err != nil {
		return
	}
	var resources map[string]string
	if attrs.HostID != "" {
		resources = map[string]string{resourceInstanceID: attrs.HostID}
	}
	collectors, scrapeWait := telemetry.BuildCollectors(cfg, source, resources, logger)

	logger.Info("starting node telemetry", "interval", cfg.ReportInterval(), "collectors", len(collectors), "attributes", attrs)
	_ = telemetry.NewManager(telemetry.ManagerConfig{
		Interval:   cfg.ReportInterval(),
		Node:       attrs,
		Collectors: collectors,
		ScrapeWait: scrapeWait,
		Sender:     sender,
		Registerer: metrics.Registry,
		Logger:     logger,
	}).Start(ctx)
}
