package workload

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var testParams = Params{Namespace: DefaultNamespace, EFSFileSystemID: "fs-0123456789abcdef0"}

func TestObjects(t *testing.T) {
	objs, err := Objects(testParams)
	require.NoError(t, err)
	require.NotEmpty(t, objs)
	assert.Equal(t, "Namespace", objs[0].GetKind(), "the namespace must be applied first")

	var backgroundReplicas int32
	kinds := map[string]int{}
	for _, obj := range objs {
		kinds[obj.GetKind()]++
		assert.Equal(t, PartOfValue, obj.GetLabels()[PartOfLabel], "%s %s is missing the part-of label", obj.GetKind(), obj.GetName())
		if obj.GetKind() == "Namespace" || obj.GetKind() == "StorageClass" {
			continue
		}
		assert.Equal(t, DefaultNamespace, obj.GetNamespace(), "%s %s", obj.GetKind(), obj.GetName())

		if obj.GetKind() == "Deployment" {
			var dep appsv1.Deployment
			require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &dep))
			require.NotNil(t, dep.Spec.Replicas)
			backgroundReplicas += *dep.Spec.Replicas
			assert.Equal(t, SoakPool, dep.Spec.Template.Spec.NodeSelector[PoolLabel], "deployment %s must run on the soak pool", dep.Name)
			assert.Equal(t, dep.Spec.Selector.MatchLabels, dep.Spec.Template.Labels, "deployment %s selector must match its pods", dep.Name)
			for _, c := range dep.Spec.Template.Spec.Containers {
				assert.False(t, c.Resources.Limits.Memory().IsZero(), "deployment %s container %s needs a memory limit", dep.Name, c.Name)
			}
		}
	}
	assert.EqualValues(t, 1000, backgroundReplicas, "background deployments should total 1000 pods")
	for _, kind := range []string{"Namespace", "Deployment", "CronJob", "StorageClass", "PersistentVolumeClaim", "ServiceAccount", "NetworkPolicy"} {
		assert.Positive(t, kinds[kind], "expected at least one %s", kind)
	}
}

func TestObjectsRendersEFSFileSystemID(t *testing.T) {
	objs, err := Objects(testParams)
	require.NoError(t, err)
	for _, obj := range objs {
		if obj.GetKind() == "StorageClass" {
			params, _ := obj.Object["parameters"].(map[string]interface{})
			assert.Equal(t, testParams.EFSFileSystemID, params["fileSystemId"])
			return
		}
	}
	t.Fatal("no StorageClass rendered")
}

// Every churn flavor must exercise the same set of NMA code paths: an EFS
// mount, a VPC (ENI) IP, a projected service-account token, and placement on
// the churn node.
func TestChurnPodTemplates(t *testing.T) {
	pods, err := ChurnPodTemplates(testParams)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(pods), 3, "churn should have several flavors")

	containerCounts := map[int]bool{}
	for _, pod := range pods {
		spec := pod.Spec
		assert.NotEmpty(t, pod.Labels[FlavorLabel], "%s needs a flavor label", pod.Name)
		assert.False(t, spec.HostNetwork, "%s must get a VPC IP, not the host network", pod.Name)
		assert.Equal(t, corev1.RestartPolicyNever, spec.RestartPolicy, pod.Name)
		assert.Equal(t, ChurnPool, spec.NodeSelector[PoolLabel], "%s must run on the churn pool", pod.Name)
		assert.Contains(t, spec.Tolerations, corev1.Toleration{
			Key: PoolLabel, Operator: corev1.TolerationOpEqual, Value: ChurnPool, Effect: corev1.TaintEffectNoSchedule,
		}, pod.Name)
		assert.NotEmpty(t, spec.ServiceAccountName, pod.Name)

		var hasEFS, hasToken bool
		for _, v := range spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "nma-soak-efs" {
				hasEFS = true
			}
			if v.Projected != nil {
				for _, src := range v.Projected.Sources {
					if src.ServiceAccountToken != nil {
						hasToken = true
						require.NotNil(t, src.ServiceAccountToken.ExpirationSeconds, pod.Name)
					}
				}
			}
		}
		assert.True(t, hasEFS, "%s must mount the EFS volume", pod.Name)
		assert.True(t, hasToken, "%s must mount a projected service-account token", pod.Name)
		for _, c := range append(spec.InitContainers, spec.Containers...) {
			assert.False(t, c.Resources.Limits.Memory().IsZero(), "%s container %s needs a memory limit", pod.Name, c.Name)
		}
		containerCounts[len(spec.Containers)] = true
	}
	assert.GreaterOrEqual(t, len(containerCounts), 2, "churn flavors should vary in container count")
}
