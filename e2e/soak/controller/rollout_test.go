package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func agentDaemonSet(image string, status appsv1.DaemonSetStatus) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultAgentDaemonSet, Namespace: "kube-system", Generation: 2},
		Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: DefaultAgentContainer, Image: image}},
		}}},
		Status: status,
	}
}

func TestDaemonSetRollout(t *testing.T) {
	complete := appsv1.DaemonSetStatus{ObservedGeneration: 2, DesiredNumberScheduled: 11, UpdatedNumberScheduled: 11, NumberAvailable: 11}
	for name, tc := range map[string]struct {
		ds       *appsv1.DaemonSet
		image    string
		done     bool
		contains string
	}{
		"complete":             {ds: agentDaemonSet(image, complete), image: image, done: true},
		"any image when unset": {ds: agentDaemonSet("other:tag", complete), done: true},
		"old image":            {ds: agentDaemonSet("old:tag", complete), image: image, contains: "want"},
		"not observed": {ds: agentDaemonSet(image, func() appsv1.DaemonSetStatus { s := complete; s.ObservedGeneration = 1; return s }()),
			image: image, contains: "observed"},
		"updating": {ds: agentDaemonSet(image, func() appsv1.DaemonSetStatus { s := complete; s.UpdatedNumberScheduled = 4; return s }()),
			image: image, contains: "4 of 11 agent pods updated"},
		"unavailable": {ds: agentDaemonSet(image, func() appsv1.DaemonSetStatus { s := complete; s.NumberAvailable = 10; return s }()),
			image: image, contains: "10 of 11 agent pods available"},
		"nothing scheduled": {ds: agentDaemonSet(image, appsv1.DaemonSetStatus{ObservedGeneration: 2}), image: image, contains: "no agent pods"},
	} {
		t.Run(name, func(t *testing.T) {
			r := DaemonSetRollout{Client: fake.NewClientset(tc.ds), Namespace: "kube-system", Name: DefaultAgentDaemonSet, Container: DefaultAgentContainer}
			done, detail, err := r.RolledOut(context.Background(), tc.image)
			require.NoError(t, err)
			assert.Equal(t, tc.done, done)
			assert.Contains(t, detail, tc.contains)
		})
	}

	t.Run("missing daemonset", func(t *testing.T) {
		r := DaemonSetRollout{Client: fake.NewClientset(), Namespace: "kube-system", Name: DefaultAgentDaemonSet, Container: DefaultAgentContainer}
		done, detail, err := r.RolledOut(context.Background(), image)
		require.NoError(t, err)
		assert.False(t, done)
		assert.Contains(t, detail, "not found")
	})
}
