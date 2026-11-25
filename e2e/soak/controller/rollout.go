package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	DefaultAgentDaemonSet = "eks-node-monitoring-agent"
	DefaultAgentContainer = "eks-node-monitoring-agent"
)

// DaemonSetRollout checks the agent DaemonSet's rollout status.
type DaemonSetRollout struct {
	Client    kubernetes.Interface
	Namespace string
	Name      string
	Container string
}

// RolledOut reports whether the DaemonSet's template runs imageURI (when set)
// and every scheduled pod is updated and available.
func (d DaemonSetRollout) RolledOut(ctx context.Context, imageURI string) (bool, string, error) {
	ds, err := d.Client.AppsV1().DaemonSets(d.Namespace).Get(ctx, d.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, fmt.Sprintf("daemonset %s/%s not found", d.Namespace, d.Name), nil
	}
	if err != nil {
		return false, "", err
	}
	if imageURI != "" {
		var image string
		for _, c := range ds.Spec.Template.Spec.Containers {
			if c.Name == d.Container {
				image = c.Image
			}
		}
		if image != imageURI {
			return false, fmt.Sprintf("daemonset runs %q, want %q", image, imageURI), nil
		}
	}
	s := ds.Status
	switch {
	case s.ObservedGeneration < ds.Generation:
		return false, "daemonset controller has not observed the latest spec", nil
	case s.DesiredNumberScheduled == 0:
		return false, "no agent pods are scheduled", nil
	case s.UpdatedNumberScheduled < s.DesiredNumberScheduled:
		return false, fmt.Sprintf("%d of %d agent pods updated", s.UpdatedNumberScheduled, s.DesiredNumberScheduled), nil
	case s.NumberAvailable < s.DesiredNumberScheduled:
		return false, fmt.Sprintf("%d of %d agent pods available", s.NumberAvailable, s.DesiredNumberScheduled), nil
	}
	return true, "", nil
}
