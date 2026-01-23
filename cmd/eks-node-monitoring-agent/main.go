package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/pflag"
	zapraw "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"golang.a2z.com/Eks-node-monitoring-agent/api/v1alpha1"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/manager"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/registry"

	// Import monitor packages to trigger auto-registration via init()
	_ "golang.a2z.com/Eks-node-monitoring-agent/monitors/kernel"
	// Import observer packages to register observers
	_ "golang.a2z.com/Eks-node-monitoring-agent/pkg/observer"
)

var (
	controllerHealthProbeAddress string
	controllerMetricsAddress     string
	controllerPprofAddress       string
	hostname                     string
	verbosity                    int
)

const (
	envNodeName = "MY_NODE_NAME"
)

func init() {
	utilruntime.Must(v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme))
}

func main() {
	utilruntime.Must(run())
}

func run() error {
	if err := parseFlags(); err != nil {
		return err
	}
	if err := ensureHostname(); err != nil {
		return err
	}

	logger := zap.New(zap.Level(zapraw.NewAtomicLevelAt(zapcore.Level(-verbosity)))).WithValues("hostname", hostname)
	log.SetLogger(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	logger.Info("initializing controller manager")
	mgr, err := controllerruntime.NewManager(controllerruntime.GetConfigOrDie(), controllerruntime.Options{
		Logger:                 log.FromContext(ctx),
		Scheme:                 scheme.Scheme,
		HealthProbeBindAddress: controllerHealthProbeAddress,
		BaseContext:            func() context.Context { return ctx },
		Metrics:                server.Options{BindAddress: controllerMetricsAddress},
		PprofBindAddress:       controllerPprofAddress,
	})
	if err != nil {
		logger.Error(err, "failed to create controller manager")
		return err
	}

	// Get all registered monitors from the global plugin registry
	// Monitors are auto-registered via init() functions when their packages are imported
	allMonitors := registry.GlobalRegistry().AllMonitors()
	if len(allMonitors) == 0 {
		logger.Info("no monitors registered - agent will run without monitoring capabilities")
		logger.Info("to add monitors, import monitor packages or register plugins using registry.Register()")
		return fmt.Errorf("no monitors registered")
	}

	logger.Info("registered monitors", "count", len(allMonitors))
	for _, mon := range allMonitors {
		logger.Info("monitor available", "name", mon.Name())
	}

	// Create node template for Kubernetes integration
	nodeTemplate := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: hostname}}

	// Get Kubernetes client and event recorder
	kubeClient := mgr.GetClient()
	eventRecorder := mgr.GetEventRecorderFor("eks-node-monitoring-agent")

	// Build condition configs for node exporter
	// Map each monitor to a Kubernetes node condition type
	conditionConfigs := make(map[corev1.NodeConditionType]manager.NodeConditionConfig)
	conditionConfigs[corev1.NodeConditionType("KernelReady")] = manager.NodeConditionConfig{
		ReadyReason:  "KernelIsReady",
		ReadyMessage: "Monitoring for the Kernel system is active",
	}
	// Add more condition types as more monitors are extracted

	// Initialize node exporter
	logger.Info("initializing node exporter")
	nodeExporter := manager.NewNodeExporter(
		nodeTemplate,
		kubeClient,
		eventRecorder,
		conditionConfigs,
	)
	go nodeExporter.Run(ctx)

	// Initialize monitoring manager
	logger.Info("initializing monitoring manager")
	monitorMgr := manager.NewMonitorManager(hostname, nodeExporter)

	// Register all monitors with the manager
	for _, mon := range allMonitors {
		monCtx := log.IntoContext(ctx, logger.WithValues("monitor", mon.Name()))
		// Map monitor name to condition type
		conditionType := "KernelReady" // For now, all monitors use KernelReady
		if err := monitorMgr.Register(monCtx, mon, conditionType); err != nil {
			logger.Error(err, "failed to register monitor", "name", mon.Name())
			return err
		}
		logger.Info("registered monitor with manager", "name", mon.Name())
	}

	// Add monitoring manager as a runnable to the controller manager
	if err := mgr.Add(monitorMgr); err != nil {
		logger.Error(err, "failed to add monitoring manager to controller")
		return err
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "failed to set up health check")
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "failed to set up ready check")
		return err
	}

	logger.Info("starting controller manager")
	return mgr.Start(ctx)
}

func parseFlags() error {
	flagSet := pflag.NewFlagSet(os.Args[0], pflag.ExitOnError)
	flagSet.AddGoFlagSet(flag.CommandLine)
	flagSet.StringVar(&hostname, "hostname-override", os.Getenv(envNodeName), "Override the default hostname for the node resource")
	flagSet.StringVar(&controllerHealthProbeAddress, "probe-address", ":8081", "Address for the controller runtime health probe endpoints")
	flagSet.StringVar(&controllerMetricsAddress, "metrics-address", ":8080", "Address for the controller runtime metrics endpoint")
	flagSet.StringVar(&controllerPprofAddress, "pprof-address", "", "Address for the controller runtime pprof endpoint (default disabled)")
	flagSet.IntVarP(&verbosity, "verbosity", "v", 2, "Logging verbosity level")
	return flagSet.Parse(os.Args[1:])
}

func ensureHostname() (err error) {
	if len(hostname) > 0 {
		return nil
	}
	// fallback to OS hostname
	hostname, err = os.Hostname()
	return err
}
