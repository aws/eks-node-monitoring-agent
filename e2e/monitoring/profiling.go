package monitoring

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/e2e-framework/klient"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
)

const (
	profilePeriod = 30 * time.Second
)

var (
	pprofAddress               string
	profileCollectionBucket    string
	profileCollectionKeyPrefix string
)

func init() {
	flag.StringVar(&pprofAddress, "pprof-address", "localhost:8082", "The pprof address of the node monitoring agent")
	flag.StringVar(&profileCollectionBucket, "profiling-bucket-name", "", "S3 bucket for collecting pprof profiles")
	flag.StringVar(&profileCollectionKeyPrefix, "profiling-bucket-key-prefix", "profiling/", "S3 bucket key prefix for collecting pprof profiles")
}

func ProfilingEnabled() bool {
	return len(profileCollectionBucket) > 0
}

// profileDaemon uses the pprof endpoint to collect profile dumps and traces
// from the agent.
type profileDaemon struct {
	cw        *cloudwatch.Client
	s3        *s3.Client
	restCfg   *rest.Config
	client    klient.Client
	daemonset *appsv1.DaemonSet
}

func NewProfileDaemon(awsCfg aws.Config, client klient.Client) *profileDaemon {
	const appName = "e2e-profiler"
	dsLabels := map[string]string{
		"app.kubernetes.io/name": appName,
	}
	return &profileDaemon{
		cw:     cloudwatch.NewFromConfig(awsCfg),
		s3:     s3.NewFromConfig(awsCfg),
		client: client,
		daemonset: &appsv1.DaemonSet{
			ObjectMeta: v1.ObjectMeta{
				Name:      appName,
				Namespace: corev1.NamespaceDefault,
			},
			Spec: appsv1.DaemonSetSpec{
				Selector: &v1.LabelSelector{
					MatchLabels: dsLabels,
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: v1.ObjectMeta{
						Name:      appName,
						Namespace: corev1.NamespaceDefault,
						Labels:    dsLabels,
					},
					Spec: corev1.PodSpec{
						// the pods needs to also be host-networking in order to
						// reach the localhost pprof endpoint from the agent.
						HostNetwork: true,
						Containers: []corev1.Container{
							{
								Name:  appName,
								Image: "public.ecr.aws/amazonlinux/amazonlinux:minimal",
								// keep alive to exec the pod periodically.
								Command: []string{"tail", "-f", "/dev/null"},
							},
						},
					},
				},
			},
		},
	}
}

func (w *profileDaemon) Cleanup(ctx context.Context) error {
	return w.client.Resources().Delete(ctx, w.daemonset)
}

func (w *profileDaemon) Start(ctx context.Context) error {
	if err := w.client.Resources().Create(ctx, w.daemonset); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return err
		}
	}
	ticker := time.NewTicker(profilePeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := w.profileNodes(ctx); err != nil {
				return err
			}
		}
	}
}

func (w *profileDaemon) profileNodes(ctx context.Context) error {
	syncTimestamp := time.Now().Format("2006-01-02.150405")

	var podList corev1.PodList
	if err := w.client.Resources().List(ctx,
		&podList,
		resources.WithLabelSelector(v1.FormatLabelSelector(w.daemonset.Spec.Selector)),
	); err != nil {
		return err
	}

	for _, pod := range podList.Items {
		execInPod := func(cmd ...string) (bytes.Buffer, error) {
			var stdout, stderr bytes.Buffer
			err := w.client.Resources().ExecInPod(ctx,
				pod.Namespace, pod.Name, pod.Spec.Containers[0].Name,
				cmd, &stdout, &stderr,
			)
			if err != nil {
				return bytes.Buffer{}, fmt.Errorf("exec command %q - %v: %s, %s", strings.Join(cmd, " "), err, stdout.String(), stderr.String())
			}
			return stdout, nil
		}

		podKey := fmt.Sprintf("%s-%s", pod.Spec.NodeName, pod.Name)

		stdout, err := execInPod("curl", pprofAddress+"/debug/pprof/heap")
		if err != nil {
			return err
		}
		if _, err := w.s3.PutObject(ctx, &s3.PutObjectInput{
			Bucket: &profileCollectionBucket,
			Key:    aws.String(filepath.Join(profileCollectionKeyPrefix, podKey, syncTimestamp+"-heap.pb.gz")),
			Body:   &stdout,
		}); err != nil {
			return err
		}

		stdout, err = execInPod("curl", pprofAddress+"/debug/pprof/profile")
		if err != nil {
			return err
		}
		if _, err := w.s3.PutObject(ctx, &s3.PutObjectInput{
			Bucket: &profileCollectionBucket,
			Key:    aws.String(filepath.Join(profileCollectionKeyPrefix, podKey, syncTimestamp+"-cpu.pb.gz")),
			Body:   &stdout,
		}); err != nil {
			return err
		}
	}
	return nil
}
