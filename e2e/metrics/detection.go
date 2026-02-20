package metrics

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

type DetectionData struct {
	ConditionType string
	Reason        string
	Delay         time.Duration
}

type PublisherStack struct {
	cw *cloudwatch.Client
}

type PublisherFnOpt func(*PublisherStack)

func WithAwsConfig(awsCfg aws.Config) PublisherFnOpt {
	return func(ps *PublisherStack) {
		ps.cw = cloudwatch.NewFromConfig(awsCfg)
	}
}

const (
	DetectionDelayMetricName     = "DetectionDelay"
	MetricDimensionConditionType = "ConditionType"
	MetricDimensionReason        = "Reason"
)

func PublishDetectionMetrics(ctx context.Context, data DetectionData, fnOpts ...PublisherFnOpt) error {
	stack := PublisherStack{}
	for _, fnOpt := range fnOpts {
		fnOpt(&stack)
	}

	if stack.cw == nil {
		awsCfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return err
		}
		stack.cw = cloudwatch.NewFromConfig(awsCfg)
	}

	_, err := stack.cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(MetricNamespace),
		MetricData: []cwtypes.MetricDatum{
			{
				MetricName: aws.String(DetectionDelayMetricName),
				Value:      aws.Float64(data.Delay.Seconds()),
				Unit:       cwtypes.StandardUnitCountSecond,
				Dimensions: []cwtypes.Dimension{
					{Name: aws.String(MetricDimensionConditionType), Value: &data.ConditionType},
					{Name: aws.String(MetricDimensionReason), Value: &data.Reason},
				},
			},
		},
	})
	return err
}
