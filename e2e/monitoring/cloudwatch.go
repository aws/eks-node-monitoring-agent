package monitoring

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"text/template"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	awshelper "github.com/aws/eks-node-monitoring-agent/e2e/aws"
)

var metricsRoleArn string

func init() {
	flag.StringVar(&metricsRoleArn, "metrics-role-arn", "", "ARN of the IAM role to use for collecting cloudwatch metrics")
}

//go:embed cloudwatch-agent-infra.yaml
var cloudwatchAgentInfraManifest string
var cloudwatchAgentInfraManifestTemplate = template.Must(template.New("cwagent").Parse(cloudwatchAgentInfraManifest))

//go:embed cloudwatch-agent.yaml
var cloudwatchAgentManifest string

type CloudwatchAgentVariables struct {
	ClusterName string
	Region      string
	MetricsHost string
}

func RenderCloudwatchAgentInfraManifest(cwagentVariables CloudwatchAgentVariables) ([]byte, error) {
	var buf bytes.Buffer
	if err := cloudwatchAgentInfraManifestTemplate.Execute(&buf, cwagentVariables); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func RenderCloudwatchAgentManifest() ([]byte, error) {
	return []byte(cloudwatchAgentManifest), nil
}

func CreateAssociation(ctx context.Context, awsCfg aws.Config, clusterName, stage string) (cleanupFn func() error, err error) {
	if metricsRoleArn == "" {
		return nil, fmt.Errorf("--metrics-role-arn cannot be empty when metrics are enabled!")
	}

	eksClient := eks.NewFromConfig(awsCfg, func(o *eks.Options) {
		if endpoint := awshelper.GetEksEndpoint(stage, "noop"); endpoint != "" {
			o.BaseEndpoint = &endpoint
		}
	})

	res, err := eksClient.CreatePodIdentityAssociation(ctx, &eks.CreatePodIdentityAssociationInput{
		ClusterName:    &clusterName,
		ServiceAccount: aws.String("cwagent-prometheus"),
		Namespace:      aws.String("amazon-cloudwatch"),
		RoleArn:        &metricsRoleArn,
	})

	if err != nil {
		var apiErr *awshttp.ResponseError
		if errors.As(err, &apiErr) && apiErr.HTTPStatusCode() == 409 {
			// if there is a 409 conflict we can reuse the existing association.
			// there will be no cleanup steps.
			return func() error { return nil }, nil
		} else {
			log.Printf("failed to create pod identity association: %v", err)
			// to be safe this path should also be a noop, and if the pod
			// identity association fails then the tests can still pass.
			return func() error { return nil }, nil
		}
	}

	return func() error {
		_, err := eksClient.DeletePodIdentityAssociation(ctx, &eks.DeletePodIdentityAssociationInput{
			AssociationId: res.Association.AssociationId,
			ClusterName:   &clusterName,
		})
		return err
	}, nil
}
