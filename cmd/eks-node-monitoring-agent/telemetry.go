package main

import (
	"context"
	"net/http"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/aws/eks-node-monitoring-agent/pkg/nodetelemetry"
	"github.com/aws/eks-node-monitoring-agent/pkg/telemetry"
	telemetryconfig "github.com/aws/eks-node-monitoring-agent/pkg/telemetry/config"
)

func runTelemetry(ctx context.Context, logger logr.Logger, cfg *telemetryconfig.Config, httpClient *http.Client, host string, kubeClient client.Client) {
	sender := nodetelemetry.NewClient(httpClient, host)
	attrs, err := telemetry.WaitForNodeAttrs(ctx, logger, kubeClient, hostname)
	if err != nil {
		return
	}
	collectors, scrapeWait := telemetry.BuildCollectors(cfg, logger)

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
