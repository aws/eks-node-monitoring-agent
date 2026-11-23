// Package workload renders and applies the Kubernetes objects that load the
// NMA soak cluster: the steady background pods on the "soak" node pool and the
// shared objects used by churn pods on the "churn" node pool.
package workload

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"text/template"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// DefaultNamespace is the namespace every soak workload runs in.
	DefaultNamespace = "nma-soak"

	// PoolLabel is the node label that selects a soak node pool. The value is
	// SoakPool or ChurnPool; churn nodes are also tainted with this key.
	PoolLabel = "nma-soak/pool"
	SoakPool  = "soak"
	ChurnPool = "churn"

	// FlavorLabel names the workload flavor of a pod.
	FlavorLabel = "nma-soak/flavor"

	PartOfLabel    = "app.kubernetes.io/part-of"
	PartOfValue    = "nma-soak"
	ComponentLabel = "app.kubernetes.io/component"
	// ChurnComponent marks pods owned by the churn driver. The churn
	// NetworkPolicy selects on it.
	ChurnComponent = "churn"

	fieldOwner = "nma-soak"
)

//go:embed manifests/*.yaml
var manifests embed.FS

// Params are the values substituted into the manifests.
type Params struct {
	Namespace string
	// EFSFileSystemID is the EFS filesystem churn pods mount. It is created
	// alongside the soak cluster.
	EFSFileSystemID string
}

func (p Params) validate() error {
	if p.Namespace == "" {
		return errors.New("namespace is required")
	}
	if p.EFSFileSystemID == "" {
		return errors.New("EFS filesystem ID is required")
	}
	return nil
}

// Objects returns the soak objects to apply, in apply order: the namespace,
// the background workload, then the shared churn objects.
func Objects(p Params) ([]*unstructured.Unstructured, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	var objs []*unstructured.Unstructured
	for _, name := range []string{"namespace.yaml", "background.yaml", "churn.yaml"} {
		data, err := render(name, p)
		if err != nil {
			return nil, err
		}
		fileObjs, err := decodeUnstructured(data)
		if err != nil {
			return nil, fmt.Errorf("decoding %s: %w", name, err)
		}
		objs = append(objs, fileObjs...)
	}
	return objs, nil
}

// ChurnPodTemplates returns the pod templates the churn driver stamps out.
func ChurnPodTemplates(p Params) ([]corev1.Pod, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	data, err := render("churn-pods.yaml", p)
	if err != nil {
		return nil, err
	}
	var pods []corev1.Pod
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var pod corev1.Pod
		if err := decoder.Decode(&pod); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decoding churn-pods.yaml: %w", err)
		}
		if pod.Name == "" {
			continue
		}
		pods = append(pods, pod)
	}
	return pods, nil
}

// Apply server-side applies the soak objects, so it is safe to call on every
// controller start and converges any drift.
func Apply(ctx context.Context, c client.Client, p Params) error {
	objs, err := Objects(p)
	if err != nil {
		return err
	}
	for _, obj := range objs {
		data, err := obj.MarshalJSON()
		if err != nil {
			return err
		}
		if err := c.Patch(ctx, obj, client.RawPatch(types.ApplyPatchType, data),
			client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
			return fmt.Errorf("applying %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
}

func render(name string, p Params) ([]byte, error) {
	raw, err := manifests.ReadFile("manifests/" + name)
	if err != nil {
		return nil, err
	}
	tpl, err := template.New(name).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, p); err != nil {
		return nil, fmt.Errorf("rendering %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

func decodeUnstructured(data []byte) ([]*unstructured.Unstructured, error) {
	var objs []*unstructured.Unstructured
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var raw runtime.RawExtension
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return objs, nil
			}
			return nil, err
		}
		if len(bytes.TrimSpace(raw.Raw)) == 0 || string(raw.Raw) == "null" {
			continue
		}
		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(raw.Raw); err != nil {
			return nil, err
		}
		objs = append(objs, obj)
	}
}
