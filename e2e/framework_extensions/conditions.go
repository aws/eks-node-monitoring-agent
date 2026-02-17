package frameworkext

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimachinerywait "k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
)

type ConditionExtension struct {
	resources *resources.Resources
}

func NewCondition(r *resources.Resources) *ConditionExtension {
	return &ConditionExtension{resources: r}
}

// EventPresent waits until an event that matches a provided boolean function
// appears in the api. Which is useful for validating that the node exporter
// pushed an event to the api server following the detection of an issue.
func (c *ConditionExtension) EventPresent(namespace string, listOptions metav1.ListOptions, fn func(corev1.Event) bool) apimachinerywait.ConditionWithContextFunc {
	return func(ctx context.Context) (done bool, err error) {
		clientcmd, err := kubernetes.NewForConfig(c.resources.GetConfig())
		if err != nil {
			return false, err
		}
		events, err := clientcmd.CoreV1().Events(namespace).List(ctx, listOptions)
		if err != nil {
			return false, err
		}
		for _, event := range events.Items {
			if fn(event) {
				return true, nil
			}
		}
		return false, nil
	}
}
