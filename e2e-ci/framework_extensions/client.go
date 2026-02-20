package frameworkext

import (
	"bytes"
	"context"
	"io"
	"text/template"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/k8s"
)

// ApplyManifests creates Kubernetes objects contained in manifests, similar to `kubectl apply -f`
func ApplyManifests(restConfig *rest.Config, manifests ...[]byte) error {
	return applyManifests(restConfig, bytesSlicesToReaders(manifests...)...)
}

func applyManifests(restConfig *rest.Config, manifests ...io.Reader) error {
	for _, manifest := range manifests {
		objs, err := decoder.DecodeAll(context.Background(), manifest)
		if err != nil {
			return err
		}
		if err := processObjects(restConfig, objs, func(client *resource.Helper, obj k8s.Object) error {
			namespace, err := meta.NewAccessor().Namespace(obj)
			if err != nil {
				return err
			}
			if namespace == "" {
				namespace = "default"
			}
			_, err = client.Create(namespace, false, obj)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteManifests deletes Kubernetes objects contained in manifests, similar to `kubectl delete -f`
func DeleteManifests(restConfig *rest.Config, manifests ...[]byte) error {
	return deleteManifests(restConfig, bytesSlicesToReaders(manifests...)...)
}

func deleteManifests(restConfig *rest.Config, manifests ...io.Reader) error {
	for _, manifest := range manifests {
		objs, err := decoder.DecodeAll(context.Background(), manifest)
		if err != nil {
			return err
		}
		if err := processObjects(restConfig, objs, func(client *resource.Helper, obj k8s.Object) error {
			name, err := meta.NewAccessor().Name(obj)
			if err != nil {
				return err
			}
			namespace, err := meta.NewAccessor().Namespace(obj)
			if err != nil {
				return err
			}
			if namespace == "" {
				namespace = "default"
			}
			deletePolicy := metav1.DeletePropagationBackground
			_, err = client.DeleteWithOptions(namespace, name, &metav1.DeleteOptions{
				PropagationPolicy: &deletePolicy,
			})
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// RenderManifests renders manifests with the supplied template data
func RenderManifests(templateStr string, data interface{}) ([]byte, error) {
	tpl, err := template.New("manifest").Parse(templateStr)
	if err != nil {
		return nil, err
	}
	buf := bytes.Buffer{}
	err = tpl.Execute(&buf, data)
	return buf.Bytes(), err
}

func bytesSlicesToReaders(byteSlices ...[]byte) []io.Reader {
	var readers []io.Reader
	for _, b := range byteSlices {
		readers = append(readers, bytes.NewReader(b))
	}
	return readers
}

func processObjects(restConfig *rest.Config, objs []k8s.Object, processFunc func(client *resource.Helper, obj k8s.Object) error) error {
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}
	groupResources, err := restmapper.GetAPIGroupResources(clientset.Discovery())
	if err != nil {
		return err
	}
	rm := restmapper.NewDiscoveryRESTMapper(groupResources)
	for _, obj := range objs {
		client, err := newResourceHelper(restConfig, rm, obj)
		if err != nil {
			return err
		}
		if err := processFunc(client, obj); err != nil {
			return err
		}
	}
	return nil
}

func newResourceHelper(restConfig *rest.Config, rm meta.RESTMapper, obj runtime.Object) (*resource.Helper, error) {
	gvk := obj.GetObjectKind().GroupVersionKind()
	gk := schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}
	mapping, err := rm.RESTMapping(gk, gvk.Version)
	if err != nil {
		return nil, err
	}
	gv := mapping.GroupVersionKind.GroupVersion()
	restConfig.ContentConfig = resource.UnstructuredPlusDefaultContentConfig()
	restConfig.GroupVersion = &gv
	if len(gv.Group) == 0 {
		restConfig.APIPath = "/api"
	} else {
		restConfig.APIPath = "/apis"
	}
	restClient, err := rest.RESTClientFor(restConfig)
	if err != nil {
		return nil, err
	}
	return resource.NewHelper(restClient, mapping), nil
}
