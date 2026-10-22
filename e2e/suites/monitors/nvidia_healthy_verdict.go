package monitors

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	nodeconditions "github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

const (
	// VerdictLinePrefix starts the line NvidiaHealthyVerdict prints to stdout.
	// The continuous GPU validation workflow reads this line from the test
	// logs; it does not rely on the exit code, which kubetest2 does not pass
	// through.
	VerdictLinePrefix = "NMA_VERDICT="
	VerdictHealthy    = "HEALTHY"
	VerdictUnhealthy  = "UNHEALTHY"

	// nvidiaNodeWaitTimeout bounds the wait for NMA to report
	// AcceleratedHardwareReady on an NVIDIA node.
	nvidiaNodeWaitTimeout = 5 * time.Minute
	// healthyVerdictPollInterval is how often the condition is read while it
	// is being watched.
	healthyVerdictPollInterval = 30 * time.Second
	// maxConsecutiveReadErrors is how many polls in a row may fail to read a
	// node before the test gives up without a verdict.
	maxConsecutiveReadErrors = 4

	neuronReasonPrefix = "Neuron"
)

// NvidiaHealthyVerdict checks NMA's verdict on NVIDIA hardware that is expected
// to be healthy. It is run by the continuous GPU validation workflow, so it
// must not disturb the node: it injects no faults, replaces no nodes, and does
// not touch DCGM.
//
// NMA starts AcceleratedHardwareReady as True before any check has run, and
// reads the DCGM fields on a 5-minute tick whose first tick can start up to a
// minute late. A single early read therefore proves nothing. The test watches
// the condition for observeFor and requires it to stay True on every NVIDIA
// node. Use at least 15 minutes, so the fields are read at least twice.
//
// It prints "NMA_VERDICT=HEALTHY" or "NMA_VERDICT=UNHEALTHY" only when NMA gave
// a verdict. When it cannot get one (no NVIDIA node, a node goes away or stops
// being Ready, the API cannot be read) it fails without printing the line, so
// the workflow records no result instead of a wrong verdict.
func NvidiaHealthyVerdict(observeFor time.Duration) types.Feature {
	var nodeNames []string

	return features.New("NvidiaHealthyVerdict").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			err := wait.For(
				func(ctx context.Context) (bool, error) {
					names, err := nvidiaNodeNames(ctx, cfg)
					if err != nil {
						t.Logf("listing nodes: %v", err)
						return false, nil
					}
					nodeNames = names
					return len(names) > 0, nil
				},
				wait.WithTimeout(nvidiaNodeWaitTimeout),
				wait.WithInterval(10*time.Second),
				wait.WithContext(ctx),
			)
			if err != nil {
				t.Fatalf("no node reported the Nvidia %s condition within %s, so there is no verdict: %v",
					nodeconditions.AcceleratedHardwareReady, nvidiaNodeWaitTimeout, err)
			}
			t.Logf("watching %s on %v for %s", nodeconditions.AcceleratedHardwareReady, nodeNames, observeFor)
			return ctx
		}).
		Assess("StaysHealthy", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			deadline := time.Now().Add(observeFor)
			ticker := time.NewTicker(healthyVerdictPollInterval)
			defer ticker.Stop()

			readErrors := 0
			for {
				unhealthy, err := checkNvidiaNodes(ctx, t, cfg, nodeNames)
				switch {
				case err != nil:
					readErrors++
					t.Logf("poll failed (%d in a row): %v", readErrors, err)
					if readErrors >= maxConsecutiveReadErrors {
						t.Fatalf("could not read the nodes %d times in a row, so there is no verdict: %v", readErrors, err)
					}
				case unhealthy:
					printVerdict(VerdictUnhealthy)
					t.Fatalf("NMA reported %s False on healthy hardware", nodeconditions.AcceleratedHardwareReady)
				default:
					readErrors = 0
				}

				if !time.Now().Before(deadline) {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatalf("stopped before the %s window ended, so there is no verdict: %v", observeFor, ctx.Err())
				case <-ticker.C:
				}
			}

			if readErrors > 0 {
				t.Fatalf("the last poll could not read the nodes, so there is no verdict")
			}
			printVerdict(VerdictHealthy)
			return ctx
		}).
		Feature()
}

// checkNvidiaNodes reads each node once. It returns unhealthy=true when NMA
// reports AcceleratedHardwareReady False on any of them. It fails the test
// without a verdict when a node is gone, being deleted, not Ready, or no longer
// reports the condition, because then NMA's reading cannot be trusted. It
// returns an error only for a failed API read, which the caller may retry.
func checkNvidiaNodes(ctx context.Context, t *testing.T, cfg *envconf.Config, nodeNames []string) (unhealthy bool, err error) {
	for _, name := range nodeNames {
		var node corev1.Node
		if err := cfg.Client().Resources().Get(ctx, name, "", &node); err != nil {
			return false, fmt.Errorf("reading node %q: %w", name, err)
		}
		if node.DeletionTimestamp != nil {
			t.Fatalf("node %q is being deleted, so there is no verdict", name)
		}
		if ready := GetNodeStatusCondition(&node, isConditionType(corev1.NodeReady)); ready == nil || ready.Status != corev1.ConditionTrue {
			t.Fatalf("node %q is not Ready, so there is no verdict", name)
		}

		condition := GetNodeStatusCondition(&node, isConditionType(nodeconditions.AcceleratedHardwareReady))
		if condition == nil {
			t.Fatalf("node %q no longer reports %s, so there is no verdict", name, nodeconditions.AcceleratedHardwareReady)
		}
		switch condition.Status {
		case corev1.ConditionTrue:
		case corev1.ConditionFalse:
			t.Logf("node %q: %s=False reason=%s message=%q",
				name, nodeconditions.AcceleratedHardwareReady, condition.Reason, condition.Message)
			unhealthy = true
		default:
			t.Fatalf("node %q reports %s=%s, so there is no verdict",
				name, nodeconditions.AcceleratedHardwareReady, condition.Status)
		}
	}
	return unhealthy, nil
}

// nvidiaNodeNames returns the nodes on which NMA reports
// AcceleratedHardwareReady for NVIDIA hardware. NMA adds the condition only on
// accelerated nodes, and every Neuron reason starts with "Neuron", so any other
// reason (healthy or not) means an NVIDIA node.
func nvidiaNodeNames(ctx context.Context, cfg *envconf.Config) ([]string, error) {
	var nodeList corev1.NodeList
	if err := cfg.Client().Resources().List(ctx, &nodeList); err != nil {
		return nil, err
	}
	var names []string
	for _, node := range nodeList.Items {
		condition := GetNodeStatusCondition(&node, isConditionType(nodeconditions.AcceleratedHardwareReady))
		if condition != nil && !strings.HasPrefix(condition.Reason, neuronReasonPrefix) {
			names = append(names, node.Name)
		}
	}
	return names, nil
}

func isConditionType(conditionType corev1.NodeConditionType) func(corev1.NodeCondition) bool {
	return func(nc corev1.NodeCondition) bool { return nc.Type == conditionType }
}

// printVerdict writes the verdict line straight to stdout, not through t.Log,
// so it starts at the beginning of a line whether or not --test.v is set.
func printVerdict(verdict string) {
	fmt.Println(VerdictLinePrefix + verdict)
}
