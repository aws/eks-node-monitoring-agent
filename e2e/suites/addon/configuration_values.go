package addon

import (
	"context"
	_ "embed"
	"strings"
	"testing"
	"time"

	awshelper "github.com/aws/eks-node-monitoring-agent/e2e/aws"
	k8shelper "github.com/aws/eks-node-monitoring-agent/e2e/k8s"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

//go:embed configuration_values.json
var configurationValuesJson string

func ConfigurationValues(stage string, awsCfg aws.Config) types.Feature {
	eksClient := eks.NewFromConfig(awsCfg, func(o *eks.Options) {
		if endpoint := awshelper.GetEksEndpoint(stage, "noop"); endpoint != "" {
			o.BaseEndpoint = &endpoint
		}
	})

	var describeAddonResponse *eks.DescribeAddonOutput

	return features.New("ConfigurationValues").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			clusterName, err := k8shelper.ExtractClusterName(c.KubeContext())
			if err != nil {
				t.Fatal(err)
			}
			describeAddonResponse, err = eksClient.DescribeAddon(ctx, &eks.DescribeAddonInput{
				AddonName:   aws.String("eks-node-monitoring-agent"),
				ClusterName: clusterName,
			})
			if err != nil {
				// TODO: handle this more elegantly.
				if strings.Contains(err.Error(), "No addon: eks-node-monitoring-agent found in cluster") {
					t.Skip("agent is not installed as an EKS Addon")
				}
				t.Fatal(err)
			}
			return ctx
		}).
		AssessWithDescription("UpdateSpecs", "Validate EKS addon configurations are applied to DaemonSet Specs", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			t.Log("updating EKS addon configuration..")
			_, err := eksClient.UpdateAddon(ctx, &eks.UpdateAddonInput{
				ConfigurationValues: aws.String(configurationValuesJson),
				AddonName:           describeAddonResponse.Addon.AddonName,
				ClusterName:         describeAddonResponse.Addon.ClusterName,
				AddonVersion:        describeAddonResponse.Addon.AddonVersion,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Log("finished updating EKS addon configuration, starting validation steps")
			defer func() {
				t.Log("restoring prior EKS addon configuration..")
				err := wait.For(
					func(ctx context.Context) (done bool, err error) {
						_, err = eksClient.UpdateAddon(ctx, &eks.UpdateAddonInput{
							// NOTE: if the configuration was empty we want to
							// revert back to that state, but a nil string
							// pointer applies no changes.
							ConfigurationValues: aws.String(aws.ToString(describeAddonResponse.Addon.ConfigurationValues)),
							AddonName:           describeAddonResponse.Addon.AddonName,
							ClusterName:         describeAddonResponse.Addon.ClusterName,
							AddonVersion:        describeAddonResponse.Addon.AddonVersion,
						})
						return err == nil, nil
					},
					wait.WithInterval(30*time.Second),
					wait.WithTimeout(5*time.Minute),
				)

				if err != nil {
					t.Error(err)
				}
			}()

			if err := validateMonitoringAgent(ctx, c); err != nil {
				t.Error(err)
			}
			if err := validateDcgmServer(ctx, c); err != nil {
				t.Error(err)
			}

			return ctx
		}).
		Feature()
}

func validateMonitoringAgent(ctx context.Context, c *envconf.Config) error {
	return wait.For(
		conditions.New(c.Client().Resources()).ResourceMatch(&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "eks-node-monitoring-agent",
				Namespace: metav1.NamespaceSystem,
			},
		}, func(object k8s.Object) bool {
			ds := object.(*appsv1.DaemonSet)
			// all limits and requests should equal 999
			for _, container := range ds.Spec.Template.Spec.Containers {
				for _, quantity := range []*resource.Quantity{
					container.Resources.Limits.Cpu(),
					container.Resources.Limits.Memory(),
					container.Resources.Requests.Cpu(),
					container.Resources.Requests.Memory(),
				} {
					if !strings.HasPrefix(quantity.String(), "999") {
						return false
					}
				}
			}
			return true
		}),
		wait.WithContext(ctx),
		wait.WithTimeout(5*time.Minute),
	)
}

func validateDcgmServer(ctx context.Context, c *envconf.Config) error {
	return wait.For(
		conditions.New(c.Client().Resources()).ResourceMatch(&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dcgm-server",
				Namespace: metav1.NamespaceSystem,
			},
		}, func(object k8s.Object) bool {
			ds := object.(*appsv1.DaemonSet)
			// all limits and requests should equal 999
			for _, container := range ds.Spec.Template.Spec.Containers {
				for _, quantity := range []*resource.Quantity{
					container.Resources.Limits.Cpu(),
					container.Resources.Limits.Memory(),
					container.Resources.Requests.Cpu(),
					container.Resources.Requests.Memory(),
				} {
					if !strings.HasPrefix(quantity.String(), "999") {
						return false
					}
				}
			}
			return true
		}),
		wait.WithContext(ctx),
		wait.WithTimeout(5*time.Minute),
	)
}
