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
	clusterArn, err := arn.Parse(config.Contexts[config.CurrentContext].Cluster)
	if err != nil {
		return nil, err
	}
	clusterName := strings.Split(clusterArn.Resource, "/")[1]
	return &clusterName, nil
}
