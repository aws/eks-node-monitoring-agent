package setup

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	"github.com/aws/eks-node-monitoring-agent/e2e-ci/suites/basic"
)

//go:embed manifests/agent.tpl.yaml
var agentManifestTemplateData string

var (
	installAgent             bool
	nodeMonitoringAgentImage string
)

// Configure sets up the test environment and returns setup/finish functions.
func Configure() (testenv env.Environment, setupFuncs []env.Func, finishFuncs []env.Func) {
	flag.BoolVar(&installAgent, "install", true, "Whether to install the node monitoring agent manifests")
	flag.StringVar(&nodeMonitoringAgentImage, "image", "", "The node monitoring agent image reference (required when --install is true)")

	cfg, err := envconf.NewFromFlags()
	if err != nil {
		log.Fatalf("failed to initialize test environment: %v", err)
	}

	// Install agent manifests if requested
	if installAgent {
		if len(nodeMonitoringAgentImage) == 0 {
			log.Fatalf("'--image' must be provided when --install is true")
		}
		setupHook, finishHook := makeInstallAgentHooks(nodeMonitoringAgentImage)
		setupFuncs = append(setupFuncs, setupHook)
		finishFuncs = append(finishFuncs, finishHook)
	}

	// Wait for DaemonSet to be ready
	setupFuncs = append(setupFuncs,
		func(ctx context.Context, config *envconf.Config) (context.Context, error) {
			ds := appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "eks-node-monitoring-agent",
					Namespace: "kube-system",
				},
			}
			log.Printf("waiting for DaemonSet %s/%s to be ready...", ds.Namespace, ds.Name)
			if err := wait.For(
				conditions.New(cfg.Client().Resources()).DaemonSetReady(&ds),
				wait.WithTimeout(5*time.Minute),
			); err != nil {
				return ctx, fmt.Errorf("failed waiting for DaemonSet to become ready: %w", err)
			}
			log.Printf("DaemonSet is ready")
			return ctx, nil
		},
	)

	return env.NewWithConfig(cfg), setupFuncs, finishFuncs
}

// TestWrapper runs all test suites.
func TestWrapper(t *testing.T, testenv env.Environment) {
	// Basic deployment validation tests
	testenv.Test(t,
		basic.DaemonSetReady(),
		basic.PodsHealthy(),
		basic.CRDsInstalled(),
	)
}
