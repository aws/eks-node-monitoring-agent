package monitors

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	nodeconditions "golang.a2z.com/Eks-node-monitoring-agent/pkg/conditions"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

func NvidiaMonitor() types.Feature {
	var targetNode *corev1.Node

	return features.New("NvidiaMonitor").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var nodeList corev1.NodeList
			if err := cfg.Client().Resources().List(ctx, &nodeList); err != nil {
				t.Fatal(err)
			}
			// search through the nodes in the cluster to see if any are nvidia
			// variants. we can tell by using the nodeCondition message.
			for _, node := range nodeList.Items {
				condition := GetNodeStatusCondition(&node, func(nc corev1.NodeCondition) bool { return nc.Type == nodeconditions.AcceleratedHardwareReady })
				if condition != nil {
					if condition.Status != corev1.ConditionTrue {
						t.Fatalf("status of condition %+v was not %s", condition, corev1.ConditionTrue)
					}
					if strings.Contains(condition.Message, "Nvidia") {
						targetNode = &node
						break
					}
				}
			}
			if targetNode == nil {
				t.Skipf("skipping because none of the nodes are running the nvidia monitor")
			}
			t.Logf("targetting node %q for test", targetNode.Name)
			return ctx
		}).
		Assess("DoubleBitError", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			startTime := metav1.Now()
			dcgmiArgs := []string{"test", "--inject", "--gpuid", "0", "-f", "319", "-v", "1"}
			t.Logf("Injecting DCGM error code via dcgmi args: %v", dcgmiArgs)
			if err := dcgmiExec(ctx, t, cfg, targetNode.Name, dcgmiArgs); err != nil {
				t.Fatal(err)
			}
			if err := wait.For(
				nodeConditionWaiter(ctx,
					conditions.New(cfg.Client().Resources()), targetNode,
					startTime.Time, nodeconditions.AcceleratedHardwareReady, "NvidiaDoubleBitError"),
				wait.WithTimeout(time.Minute),
				wait.WithContext(ctx),
			); err != nil {
				t.Fatal(err)
			}
			return ctx
		}).
		Assess("DCGMError", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// try to find dcgm-server pods to recreate a DCGM failure
			if _, ok := getDcgmPod(ctx, t, cfg, targetNode.Name); !ok {
				t.Skip("dcgm-server pod does not exist. skipping test to mock DCGM failure.")
			}

			dcgmServerDs := appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dcgm-server",
					Namespace: "kube-system",
				},
			}
			if err := cfg.Client().Resources().Get(ctx, dcgmServerDs.Name, dcgmServerDs.Namespace, &dcgmServerDs); err != nil {
				t.Fatal(err)
			}
			dcgmServerDsStored := dcgmServerDs.DeepCopy()

			// modify the daemonset to not scheduled into any nodes by using a
			// non-existent node label.
			dcgmServerDs.Spec.Template.Spec.NodeSelector["non-existing"] = "true"
			t.Log("unscheduling dcgm-server pods using nodeSelector")
			if err := cfg.Client().Resources().Update(ctx, &dcgmServerDs); err != nil {
				t.Fatal(err)
			}
			// revert the changes to the daemonset once the test is over.
			defer func() {
				t.Log("reverting changes to reschedule dcgm-server pods")
				if err := cfg.Client().Resources().Get(ctx, dcgmServerDs.Name, dcgmServerDs.Namespace, &dcgmServerDs); err != nil {
					t.Fatal(err)
				}
				// restore the old settings.
				dcgmServerDs.Spec = dcgmServerDsStored.Spec
				if err := cfg.Client().Resources().Update(ctx, &dcgmServerDs); err != nil {
					t.Fatal(err)
				}
			}()

			if err := wait.For(
				nodeConditionWaiter(ctx,
					conditions.New(cfg.Client().Resources()), targetNode,
					time.Now(), nodeconditions.AcceleratedHardwareReady, "DCGMError"),
				wait.WithTimeout(5*time.Minute),
				wait.WithContext(ctx),
			); err != nil {
				t.Fatal(err)
			}
			return ctx
		}).
		Feature()
}

func getDcgmPod(ctx context.Context, t *testing.T, cfg *envconf.Config, nodeName string) (*corev1.Pod, bool) {
	// TODO: make this more dynamic, the dcgm pod is something deployed
	// with the agent, so we are just assuming the variables are
	// consistent here.
	var podList corev1.PodList
	if err := cfg.Client().Resources("kube-system").List(ctx, &podList,
		resources.WithFieldSelector("spec.nodeName="+nodeName),
		resources.WithLabelSelector("k8s-app=dcgm-server"),
	); err != nil {
		t.Logf("failed to list dcgm-server pods: %s", err)
		return nil, false
	}
	if len(podList.Items) == 0 {
		t.Log("could not find dcgm-server pod. the daemonset may not be deployed")
		return nil, false
	}
	dcgmPod := podList.Items[0]
	return &dcgmPod, true
}

func dcgmiExec(ctx context.Context, t *testing.T, cfg *envconf.Config, nodeName string, dcgmiArgs []string) error {
	// try to find dcgm-server pods to exec into first, then fall back
	// to running a pod using binaries on the host.
	if dcgmServerPod, ok := getDcgmPod(ctx, t, cfg, nodeName); ok {
		return dcgmiPodExec(ctx, t, cfg, dcgmServerPod, dcgmiArgs)
	} else {
		return dcgmiHostExec(ctx, cfg, nodeName, dcgmiArgs)
	}
}

func dcgmiPodExec(ctx context.Context, t *testing.T, cfg *envconf.Config, dcgmPod *corev1.Pod, dcgmiArgs []string) error {
	var out bytes.Buffer
	err := cfg.Client().Resources().ExecInPod(ctx,
		dcgmPod.Namespace, dcgmPod.Name, dcgmPod.Spec.Containers[0].Name,
		append([]string{"dcgmi"}, dcgmiArgs...), &out, &out,
	)
	t.Logf("dcgmi logs:\n%s", out.String())
	return err
}

func dcgmiHostExec(ctx context.Context, cfg *envconf.Config, nodeName string, dcgmiArgs []string) error {
	const podName = "dcgmi-host-pod"
	job := batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: corev1.NamespaceDefault,
		},
		Spec: batchv1.JobSpec{
			Completions:             aws.Int32(1),
			TTLSecondsAfterFinished: aws.Int32(60),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeName: nodeName,
					// needs to connect to nv-hostengine running on localhost.
					HostNetwork: true,
					Containers: []corev1.Container{
						{
							Name: podName,
							// required shared libaries on the host only work if
							// properly chrooted first into the host root.
							Command:         append([]string{"chroot", hostRootMount.MountPath, "/usr/bin/dcgmi"}, dcgmiArgs...),
							Image:           "public.ecr.aws/amazonlinux/amazonlinux:2023-minimal",
							SecurityContext: &privilegedContext,
							VolumeMounts:    []corev1.VolumeMount{hostRootMount},
						},
					},
					Volumes:       []corev1.Volume{hostRootVolume},
					RestartPolicy: corev1.RestartPolicyOnFailure,
				},
			}},
	}
	if err := cfg.Client().Resources().Create(ctx, &job); err != nil {
		return err
	}
	defer cfg.Client().Resources().Delete(ctx, &job,
		resources.WithDeletePropagation(string(metav1.DeletePropagationForeground)))
	return wait.For(
		conditions.New(cfg.Client().Resources()).JobCompleted(&job),
		wait.WithTimeout(time.Minute),
		wait.WithContext(ctx),
	)
}
