package setup

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"testing"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awshelper "golang.a2z.com/Eks-node-monitoring-agent/e2e/aws"
	"golang.a2z.com/Eks-node-monitoring-agent/e2e/metrics"
	"golang.a2z.com/Eks-node-monitoring-agent/e2e/monitoring"
	"golang.a2z.com/Eks-node-monitoring-agent/e2e/suites/addon"
	"golang.a2z.com/Eks-node-monitoring-agent/e2e/suites/monitors"
	"golang.a2z.com/Eks-node-monitoring-agent/e2e/suites/nodediagnostic"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

var (
	//go:embed manifests/agent.tpl.yaml
	agentManifestTemplateData string
	agentManifestTemplate     = template.Must(template.New("node-monitoring-agent-daemonset").Parse(agentManifestTemplateData))
)

var (
	installAgent             bool
	nodeMonitoringAgentImage string

	eksStage string
)

var awsCfg aws.Config

func Configure() (testenv env.Environment, setupFuncs []env.Func, finishFuncs []env.Func) {
	flag.BoolVar(&installAgent, "install", true, "Whether to install the node monitoring agent manifests")
	flag.StringVar(&nodeMonitoringAgentImage, "image", "", "The node monitoring image reference. required when '--install' is true")
	flag.StringVar(&eksStage, "stage", awshelper.StageProd, "The EKS stage to use for running tests")

	cfg, err := envconf.NewFromFlags()
	if err != nil {
		log.Fatalf("failed to initialize test environment: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	awsCfg, err = config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("failed to initialize aws config: %v", err)
	}

	// this adds the manifests for the agent from the local config. If you are
	// using a cluster that already installs the monitoring agent or does so
	// using an EKS Addon, then you should disable this option.
	if installAgent {
		if len(nodeMonitoringAgentImage) == 0 {
			log.Fatalf("'--image' must be provided when --install is true")
		}
		setupHook, finishHook := makeInstallAgentHooks(nodeMonitoringAgentImage)
		setupFuncs = append(setupFuncs, setupHook)
		finishFuncs = append(finishFuncs, finishHook)
	}

	setupFuncs = append(setupFuncs,
		func(ctx context.Context, config *envconf.Config) (context.Context, error) {
			ds := appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "eks-node-monitoring-agent",
					Namespace: "kube-system",
				},
			}
			if err := wait.For(
				conditions.New(cfg.Client().Resources()).DaemonSetReady(&ds),
				// needs to be enough time to pull the container image, which
				// has heavy components from DCGM, and etc. testing recorded to
				// take upwards of 70s
				wait.WithTimeout(5*time.Minute),
			); err != nil {
				return ctx, fmt.Errorf("failed waiting for daemonset %+v to become ready: %w", ds, err)
			}

			checkDaemonSet = monitoring.NewHealthChecker(cfg, &ds).HealthCheck
			return ctx, nil
		},
	)

	if metrics.MetricsEnabled {
		cwAgentSetup, cwAgentFinish := makeCloudwatchAgentHooks()
		setupFuncs = append(setupFuncs, cwAgentSetup)
		finishFuncs = append(finishFuncs, cwAgentFinish)
	}

	if monitoring.ProfilingEnabled() {
		// collects profiles and heap dumps from the running nodes to
		// give insight into agent execution patterns.
		profileDaemon := monitoring.NewProfileDaemon(awsCfg, cfg.Client())

		setupFuncs = append(setupFuncs,
			func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
				go func() {
					log.Printf("starting profiling daemon...")
					if err := profileDaemon.Start(ctx); err != nil {
						log.Printf("profiling daemon exited: %s", err)
					}
				}()
				return ctx, nil
			},
		)
		finishFuncs = append(finishFuncs,
			func(ctx context.Context, c *envconf.Config) (context.Context, error) {
				profileDaemon.Cleanup(ctx)
				return ctx, nil
			},
		)
	}

	return env.NewWithConfig(cfg.WithParallelTestEnabled()), setupFuncs, finishFuncs
}

// gets passed into the soak test as a health check.
var checkDaemonSet func(context.Context) error

// The format of this function is a little strange because normally tests are
// run inside their own top-level test bodies.
func TestWrapper(t *testing.T, Testenv env.Environment) {
	// sleep to account for the dirty condition update time in
	// development clusters.
	time.Sleep(20 * time.Second)

	// static stability tests, which require that there is no noise.
	Testenv.Test(t,
		monitors.Soak(checkDaemonSet),
		monitors.Generic(),
	)

	// detection cases are run in parallel to speed up testing.
	Testenv.TestInParallel(t,
		monitors.ExporterImpl(),
		monitors.KernelMonitor(),
		monitors.NvidiaMonitor(),
		monitors.NeuronMonitor(),
		monitors.StressFileObserver(),
	)

	// test the addon configuration if the agent is installed as an EKS Addon.
	// this is disruptive, so it must run alone.
	Testenv.Test(t, addon.ConfigurationValues(eksStage, awsCfg))

	// log collection runs at the end, which effectively makes it collects logs
	// and data from prior tests.
	Testenv.Test(t, nodediagnostic.LogCollection(awsCfg))
}
