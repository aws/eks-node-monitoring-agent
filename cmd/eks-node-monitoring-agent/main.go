package main

import (
	"context"
	"flag"
	"os"
	"os/signal"

	"github.com/spf13/pflag"
	zapraw "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"golang.a2z.com/Eks-node-monitoring-agent/api/v1alpha1"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/monitor/registry"
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
	// Plugins should be registered via init() functions in their packages
	// or explicitly before calling main()
	allMonitors := registry.GlobalRegistry().AllMonitors()
	if len(allMonitors) == 0 {
		logger.Info("no monitors registered - agent will run without monitoring capabilities")
		logger.Info("to add monitors, register plugins using registry.Register() or registry.MustRegister()")
	}

	logger.Info("registered monitors", "count", len(allMonitors))
	for _, mon := range allMonitors {
		logger.Info("monitor available", "name", mon.Name())
	}

	// TODO: Initialize monitoring manager and register monitors
	// This will be implemented when the monitoring framework is added

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
