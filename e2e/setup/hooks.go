package setup

import (
	"context"
	"fmt"
	"log"
	"slices"
	"time"

	frameworkext "golang.a2z.com/Eks-node-monitoring-agent/e2e/framework_extensions"
	k8shelper "golang.a2z.com/Eks-node-monitoring-agent/e2e/k8s"
	"golang.a2z.com/Eks-node-monitoring-agent/e2e/monitoring"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

func makeInstallAgentHooks(image string) (setupFn, finishFn types.EnvFunc) {
	agentDaemonsetManifest, err := frameworkext.RenderManifests(agentManifestTemplateData, struct{ Image string }{
		Image: image,
	})
	if err != nil {
		log.Fatalf("failed to initialize daemonset template: %v", err)
	}

	testManifestFiles := []string{
		// TODO: don't remove, will be used later
	}
	testManifests := [][]byte{
		agentDaemonsetManifest,
	}

	setupFn = func(ctx context.Context, config *envconf.Config) (context.Context, error) {
		if err := frameworkext.ApplyManifests(config.Client().RESTConfig(), testManifests...); err != nil {
			return ctx, err
		}
		if err := frameworkext.ApplyFiles(config.Client().RESTConfig(), testManifestFiles...); err != nil {
			return ctx, err
		}
		return ctx, nil
	}

	finishFn = func(ctx context.Context, config *envconf.Config) (context.Context, error) {
		slices.Reverse(testManifests)
		if err := frameworkext.DeleteManifests(config.Client().RESTConfig(), testManifests...); err != nil {
			return ctx, err
		}
		slices.Reverse(testManifestFiles)
		if err := frameworkext.DeleteFiles(config.Client().RESTConfig(), testManifestFiles...); err != nil {
			return ctx, err
		}
		return ctx, nil
	}

	return setupFn, finishFn
}

func makeCloudwatchAgentHooks() (setupFn, finishFn types.EnvFunc) {
	var (
		cwAgentManifest      []byte
		cwAgentInfraManifest []byte
		// cleaup is a noop unless assigned.
		cleanupAssociationFn = func() error { return nil }
	)

	setupFn = func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		clusterName, err := k8shelper.ExtractClusterName(cfg.KubeContext())
		if err != nil {
			return ctx, err
		}
		cwAgentManifest, err = monitoring.RenderCloudwatchAgentManifest()
		if err != nil {
			return ctx, err
		}
		cwAgentInfraManifest, err = monitoring.RenderCloudwatchAgentInfraManifest(monitoring.CloudwatchAgentVariables{
			ClusterName: *clusterName,
			Region:      awsCfg.Region,
			// TODO: metrics port of monitoring agent. this is controlled by the
			// defaults so until this gets piped through it needs to match.
			MetricsHost: "localhost:8003",
		})
		if err != nil {
			return ctx, err
		}
		if err := frameworkext.ApplyManifests(cfg.Client().RESTConfig(), cwAgentInfraManifest); err != nil {
			return ctx, err
		}
		cleanupAssociationFn, err = monitoring.CreateAssociation(ctx, awsCfg, *clusterName, eksStage)
		if err != nil {
			return ctx, err
		}

		// give some time for the associate to progagate, otherwise the role
		// may not be applied to the pod.
		time.Sleep(5 * time.Second)

		if err := frameworkext.ApplyManifests(cfg.Client().RESTConfig(), cwAgentManifest); err != nil {
			return ctx, err
		}
		cwAgentDaemonSet := appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cwagent-prometheus",
				Namespace: "amazon-cloudwatch",
			},
		}
		if err := wait.For(
			conditions.New(cfg.Client().Resources()).DaemonSetReady(&cwAgentDaemonSet),
			wait.WithTimeout(2*time.Minute),
		); err != nil {
			return ctx, fmt.Errorf("failed waiting for cloudwatch agent daemonset %v to become ready: %w", cwAgentDaemonSet, err)
		}
		return ctx, nil
	}

	finishFn = func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		if err := frameworkext.DeleteManifests(cfg.Client().RESTConfig(), cwAgentManifest, cwAgentInfraManifest); err != nil {
			return ctx, err
		}
		if err := cleanupAssociationFn(); err != nil {
			return ctx, err
		}
		return ctx, nil
	}

	return setupFn, finishFn
}
