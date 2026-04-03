//go:build !darwin

package dcgm

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// ExpectedGPUCountProvider returns the expected GPU count for the current node.
type ExpectedGPUCountProvider interface {
	GetExpectedGPUCount(ctx context.Context) (uint, error)
}

// NewEC2ExpectedGPUCountProvider returns a provider that determines the expected
// GPU count by querying IMDS for the instance type and then calling EC2
// DescribeInstanceTypes to get the GPU specification. The result is cached after
// the first successful call since instance type never changes.
func NewEC2ExpectedGPUCountProvider() *ec2ExpectedGPUCountProvider {
	return &ec2ExpectedGPUCountProvider{}
}

type ec2ExpectedGPUCountProvider struct {
	mu       sync.Mutex
	count    uint
	resolved bool
}

func (p *ec2ExpectedGPUCountProvider) GetExpectedGPUCount(ctx context.Context) (uint, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.resolved {
		return p.count, nil
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Get instance type from IMDS
	imdsClient := imds.NewFromConfig(cfg)
	imdsResp, err := imdsClient.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "instance-type",
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get instance type from IMDS: %w", err)
	}
	defer imdsResp.Content.Close()
	body, err := io.ReadAll(imdsResp.Content)
	if err != nil {
		return 0, fmt.Errorf("failed to read IMDS response: %w", err)
	}
	instanceType := string(body)

	// Get expected GPU count from EC2 DescribeInstanceTypes
	ec2Client := ec2.NewFromConfig(cfg)
	resp, err := ec2Client.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{
		InstanceTypes: []ec2types.InstanceType{ec2types.InstanceType(instanceType)},
	})
	if err != nil {
		return 0, fmt.Errorf("failed to describe instance type %s: %w", instanceType, err)
	}
	if len(resp.InstanceTypes) == 0 {
		return 0, fmt.Errorf("no results returned for instance type %s", instanceType)
	}

	gpuInfo := resp.InstanceTypes[0].GpuInfo
	if gpuInfo == nil || len(gpuInfo.Gpus) == 0 {
		return 0, fmt.Errorf("instance type %s has no GPU information", instanceType)
	}

	var totalGPUs uint
	for _, gpu := range gpuInfo.Gpus {
		if gpu.Count != nil {
			totalGPUs += uint(*gpu.Count)
		}
	}

	p.count = totalGPUs
	p.resolved = true
	return p.count, nil
}
