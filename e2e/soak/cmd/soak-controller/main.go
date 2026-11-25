// Command soak-controller runs the NMA soak test on a long-lived soak
// cluster: it applies the background workload, churns pods on the churn
// node, samples every agent, publishes to CloudWatch, and evaluates each
// release the pipeline requests over SSM.
//
// It is configured entirely through environment variables, set by the soak
// cluster's CDK stack:
//
//	STAGE                  required, e.g. "beta"
//	EFS_FILESYSTEM_ID      required, the filesystem churn pods mount
//	SOAK_NAMESPACE         default "nma-soak"
//	SSM_REQUEST_PARAMETER  default "/nma-soak/<STAGE>/request"
//	SSM_RESULT_PARAMETER   default "/nma-soak/<STAGE>/result"
//	SOAK_WINDOW            default "5h"
//	ROLLOUT_TIMEOUT        default "30m"
//	THRESHOLDS_FILE        optional, overrides the built-in thresholds.yaml
//
// It uses in-cluster credentials, or the current kubeconfig when run locally.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/aws/eks-node-monitoring-agent/e2e/soak/analysis"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/churn"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/collector"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/controller"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/publish"
	"github.com/aws/eks-node-monitoring-agent/e2e/soak/workload"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log); err != nil {
		log.Error("soak controller failed", "error", err)
		os.Exit(1)
	}
}

type settings struct {
	stage, efsID, namespace   string
	requestParam, resultParam string
	window, rolloutTimeout    time.Duration
	thresholds                analysis.Thresholds
}

func loadSettings() (settings, error) {
	s := settings{
		stage:     os.Getenv("STAGE"),
		efsID:     os.Getenv("EFS_FILESYSTEM_ID"),
		namespace: envOr("SOAK_NAMESPACE", workload.DefaultNamespace),
	}
	var errs []error
	if s.stage == "" {
		errs = append(errs, errors.New("STAGE is required"))
	}
	if s.efsID == "" {
		errs = append(errs, errors.New("EFS_FILESYSTEM_ID is required"))
	}
	s.requestParam = envOr("SSM_REQUEST_PARAMETER", fmt.Sprintf("/nma-soak/%s/request", s.stage))
	s.resultParam = envOr("SSM_RESULT_PARAMETER", fmt.Sprintf("/nma-soak/%s/result", s.stage))

	var err error
	if s.window, err = time.ParseDuration(envOr("SOAK_WINDOW", controller.DefaultWindow.String())); err != nil {
		errs = append(errs, fmt.Errorf("SOAK_WINDOW: %w", err))
	}
	if s.rolloutTimeout, err = time.ParseDuration(envOr("ROLLOUT_TIMEOUT", controller.DefaultRolloutTimeout.String())); err != nil {
		errs = append(errs, fmt.Errorf("ROLLOUT_TIMEOUT: %w", err))
	}
	if path := os.Getenv("THRESHOLDS_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			s.thresholds, err = analysis.ParseThresholds(data)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("THRESHOLDS_FILE: %w", err))
		}
	} else if s.thresholds, err = analysis.DefaultThresholds(); err != nil {
		errs = append(errs, err)
	}
	return s, errors.Join(errs...)
}

func run(ctx context.Context, log *slog.Logger) error {
	s, err := loadSettings()
	if err != nil {
		return err
	}
	log.Info("starting soak controller", "stage", s.stage, "namespace", s.namespace,
		"window", s.window, "record_only", s.thresholds.RecordOnly)

	restCfg, err := ctrlconfig.GetConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	kube, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return err
	}
	applier, err := client.New(restCfg, client.Options{})
	if err != nil {
		return err
	}
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}

	params := workload.Params{Namespace: s.namespace, EFSFileSystemID: s.efsID}
	if err := workload.Apply(ctx, applier, params); err != nil {
		return fmt.Errorf("applying soak workload: %w", err)
	}
	templates, err := workload.ChurnPodTemplates(params)
	if err != nil {
		return err
	}
	churner, err := churn.New(kube, churn.DefaultConfig(s.namespace, templates))
	if err != nil {
		return err
	}

	c := controller.New(controller.Config{
		RequestParameter: s.requestParam,
		ResultParameter:  s.resultParam,
		Window:           s.window,
		RolloutTimeout:   s.rolloutTimeout,
		Thresholds:       s.thresholds,
	},
		controller.SSMStore{API: ssm.NewFromConfig(awsCfg)},
		collector.New(kube, collector.NewAPIServerFetcher(kube), collector.DefaultConfig()),
		publish.New(cloudwatch.NewFromConfig(awsCfg), s.stage),
		controller.DaemonSetRollout{
			Client:    kube,
			Namespace: collector.DefaultAgentNamespace,
			Name:      controller.DefaultAgentDaemonSet,
			Container: controller.DefaultAgentContainer,
		},
		churner,
		log,
	)

	churnDone := make(chan error, 1)
	go func() { churnDone <- churner.Run(ctx) }()

	err = c.Run(ctx, controller.DefaultCollectInterval, controller.DefaultTickInterval)
	stop := <-churnDone
	return errors.Join(err, stop)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
