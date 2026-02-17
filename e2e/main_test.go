//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/signal"
	"testing"

	"golang.a2z.com/Eks-node-monitoring-agent/e2e/setup"
	"sigs.k8s.io/e2e-framework/pkg/env"
)

var tenv env.Environment

func TestMain(m *testing.M) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	testenv, setupfuncs, finishfuncs := setup.Configure()
	tenv = testenv.WithContext(ctx)
	os.Exit(tenv.
		Setup(setupfuncs...).
		Finish(finishfuncs...).
		Run(m))
}

func Test(t *testing.T) {
	setup.TestWrapper(t, tenv)
}
