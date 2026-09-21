package k8s

import (
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"k8s.io/client-go/tools/clientcmd"
)

func ExtractClusterName(kubeContext string) (*string, error) {
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).RawConfig()
	if err != nil {
		return nil, err
	}
	clusterField := config.Contexts[config.CurrentContext].Cluster
	// Support to handle:
	// kubeconfigs produced by `aws eks update-kubeconfig` which uses the
	// cluster's full ARN as the context's cluster field AND,
	// kubeconfigs which use the plain cluster name directly.
	if arn.IsARN(clusterField) {
		clusterArn, err := arn.Parse(clusterField)
		if err != nil {
			return nil, err
		}
		clusterName := strings.Split(clusterArn.Resource, "/")[1]
		return &clusterName, nil
	}
	return &clusterField, nil
}
