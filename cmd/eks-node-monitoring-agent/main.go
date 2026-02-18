package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"

	"github.com/spf13/pflag"
	zapraw "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"golang.a2z.com/Eks-node-monitoring-agent/api/v1alpha1"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/conditions"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/config"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/controllers"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/manager"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/registry"

	// Import monitor packages to trigger auto-registration via init()
	_ "golang.a2z.com/Eks-node-monitoring-agent/monitors/kernel"
	_ "golang.a2z.com/Eks-node-monitoring-agent/monitors/networking"
	_ "golang.a2z.com/Eks-node-monitoring-agent/monitors/neuron"
	_ "golang.a2z.com/Eks-node-monitoring-agent/monitors/nvidia"
	_ "golang.a2z.com/Eks-node-monitoring-agent/monitors/storage"
	// Import monitors that require explicit registration (can't use init())
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/runtime"
	// Import observer packages to register observers
	_ "golang.a2z.com/Eks-node-monitoring-agent/pkg/observer"
)

var (
	controllerHealthProbeAddress string
	controllerMetricsAddress     string
	controllerPprofAddress       string
	hostname                     string
	verbosity                    int

	legacyNodeRBAC bool
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

	runtimeContext := config.GetRuntimeContext()
	logger.V(2).Info("fetched runtime context", "value", runtimeContext)

	// NOTE: this hack is needed when we are trying to use a dbus client
	// connected to the host without having chroot onto the host root. Therefore
	// its only necessary when the host root is not default.
	if config.HostRoot() != "" {
		// normally '/var/run/dbus/system_bus_socket' would be the correct path,
		// but normally there is a symlink that maps /var/run -> /run. This is
		// done with a relative path  -> ../run on Amazon Linux. but on
		// bottlerocket this is the absolute path, which results in the
		// container looking back it its own filesystem rather than the host's.
		dbusAddress := "unix:path=" + config.ToHostPath("/run/dbus/system_bus_socket")
		os.Setenv("DBUS_SYSTEM_BUS_ADDRESS", dbusAddress)
	}

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

	// Create node template for Kubernetes integration
	nodeTemplate := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: hostname}}

	monitorCfg := rest.CopyConfig(mgr.GetConfig())

	// EKS Auto has a special user impersonation flow that implicitly relies
	// on the base rest config from kubelet.
	if slices.Contains(runtimeContext.Tags(), config.EKSAuto) {
		restCfg, err := NewAutoRestConfigProvider(monitorCfg).Provide()
		if err != nil {
			logger.Error(err, "failed to provide rest config", "mode", "eks-auto")
		} else {
			monitorCfg = restCfg
		}
	} else {
		// the legacy permission model involves broader access to patch
		// node/status resources. this provider uses kubeconfig from the host
		// node in order to authenticate for self-targetted node access.
		if !legacyNodeRBAC {
			if restCfg, err := NewPodRestConfigProvider().Provide(); err != nil {
				logger.Error(err, "failed to provide rest config", "mode", "pod")
			} else {
				monitorCfg = restCfg
			}
		}
	}

	monitoringEventRecorder := mgr.GetEventRecorderFor("eks-node-monitoring-agent")
	monitoringKubeClient, err := client.New(monitorCfg, client.Options{})
	if err != nil {
		return err
	}

	for _, bootstrapper := range []Bootstrapper{
		NewHybridNodesBootstrapper(monitoringKubeClient, nodeTemplate.DeepCopy()),
	} {
		bootstrapper.Bootstrap(ctx)
	}

	// Register runtime monitor plugin manually (requires node and kubeClient dependencies)
	runtimePlugin := runtime.NewPlugin(nodeTemplate.DeepCopy(), monitoringKubeClient)
	if err := registry.ValidateAndRegister(runtimePlugin); err != nil {
		logger.Error(err, "failed to register runtime monitor plugin")
		return err
	}

	// Get all registered monitors from the global plugin registry
	allMonitors := registry.GlobalRegistry().AllMonitors()
	if len(allMonitors) == 0 {
		logger.Info("no monitors registered - agent will run without monitoring capabilities")
		return fmt.Errorf("no monitors registered")
	}

	logger.Info("registered monitors", "count", len(allMonitors))
	for _, mon := range allMonitors {
		logger.Info("monitor available", "name", mon.Name())
	}

	// Build condition configs for node exporter
	conditionConfigs := make(map[corev1.NodeConditionType]manager.NodeConditionConfig)
	conditionConfigs[conditions.KernelReady] = manager.NodeConditionConfig{
		ReadyReason:  "KernelIsReady",
		ReadyMessage: "Monitoring for the Kernel system is active",
	}
	conditionConfigs[conditions.StorageReady] = manager.NodeConditionConfig{
		ReadyReason:  "DiskIsReady",
		ReadyMessage: "Monitoring for the Disk system is active",
	}
	conditionConfigs[conditions.ContainerRuntimeReady] = manager.NodeConditionConfig{
		ReadyReason:  "ContainerRuntimeIsReady",
		ReadyMessage: "Monitoring for the ContainerRuntime system is active",
	}
	conditionConfigs[conditions.NetworkingReady] = manager.NodeConditionConfig{
		ReadyReason:  "NetworkingIsReady",
		ReadyMessage: "Monitoring for the Networking system is active",
	}

	switch runtimeContext.AcceleratedHardware() {
	case config.AcceleratedHardwareNvidia:
		conditionConfigs[conditions.AcceleratedHardwareReady] = manager.NodeConditionConfig{
			ReadyReason:  "NvidiaGPUIsReady",
			ReadyMessage: "Monitoring for the Nvidia GPU system is active",
		}
	case config.AcceleratedHardwareNeuron:
		conditionConfigs[conditions.AcceleratedHardwareReady] = manager.NodeConditionConfig{
			ReadyReason:  "NeuronAcceleratedHardwareIsReady",
			ReadyMessage: "Monitoring for the Neuron AcceleratedHardware system is active",
		}
	}

	// Initialize node exporter
	logger.Info("initializing node exporter")
	nodeExporter := manager.NewNodeExporter(
		nodeTemplate.DeepCopy(),
		monitoringKubeClient,
		monitoringEventRecorder,
		conditionConfigs,
	)
	go nodeExporter.Run(ctx)

	// Initialize monitoring manager
	logger.Info("initializing monitoring manager")
	monitorMgr := manager.NewMonitorManager(hostname, nodeExporter)

	// Register all monitors with the manager
	for _, mon := range allMonitors {
		monCtx := log.IntoContext(ctx, logger.WithValues("monitor", mon.Name()))
		var conditionType corev1.NodeConditionType
		switch mon.Name() {
		case "kernel":
			conditionType = conditions.KernelReady
		case "storage":
			conditionType = conditions.StorageReady
		case "container-runtime":
			conditionType = conditions.ContainerRuntimeReady
		case "networking":
			conditionType = conditions.NetworkingReady
		case "nvidia":
			if runtimeContext.AcceleratedHardware() != config.AcceleratedHardwareNvidia {
				logger.Info("skipping monitor registration: no nvidia hardware detected", "monitor", mon.Name())
				continue
			}
			conditionType = conditions.AcceleratedHardwareReady
		case "neuron":
			if runtimeContext.AcceleratedHardware() != config.AcceleratedHardwareNeuron {
				logger.Info("skipping monitor registration: no neuron hardware detected", "monitor", mon.Name())
				continue
			}
			conditionType = conditions.AcceleratedHardwareReady
		default:
			conditionType = conditions.KernelReady // Default fallback
		}
		if err := monitorMgr.Register(monCtx, mon, conditionType); err != nil {
			logger.Error(err, "failed to register monitor", "name", mon.Name())
			return err
		}
		logger.Info("registered monitor with manager", "name", mon.Name(), "conditionType", conditionType)
	}

	// Add monitoring manager as a runnable to the controller manager
	if err := mgr.Add(monitorMgr); err != nil {
		logger.Error(err, "failed to add monitoring manager to controller")
		return err
	}

	// Initialize and register NodeDiagnostic controller for log collection
	logger.Info("initializing node diagnostic controller")
	diagnosticController := controllers.NewNodeDiagnosticController(monitoringKubeClient, hostname, runtimeContext)
	if err := diagnosticController.Register(ctx, mgr); err != nil {
		logger.Error(err, "failed to register diagnostic controller")
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
	flagSet.BoolVar(&legacyNodeRBAC, "legacy-node-rbac", false, "Enable the legacy rbac permissions for accessing node resources")
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
