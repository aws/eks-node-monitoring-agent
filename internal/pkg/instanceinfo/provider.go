package instanceinfo

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

//go:embed instance-info.jsonl
var instanceInfoData []byte

// InstanceInfo contains hardware metadata for an EC2 instance type.
// Add new fields here as needed — existing JSONL entries without the
// field will unmarshal to the zero value.
type InstanceInfo struct {
	InstanceType string `json:"instanceType"`
	GPUCount     uint   `json:"gpuCount"`
}

// InstanceTypeInfoProvider returns hardware information about the current EC2 instance type.
type InstanceTypeInfoProvider interface {
	GetInstanceInfo(ctx context.Context) (*InstanceInfo, error)
}

// NewInstanceTypeInfoProvider returns a provider that resolves instance info
// by first checking an embedded lookup table and falling back to the EC2
// DescribeInstanceTypes API. The result is cached after the first successful
// resolution.
func NewInstanceTypeInfoProvider() *ec2InstanceTypeInfoProvider {
	return &ec2InstanceTypeInfoProvider{
		embeddedLookup: loadEmbeddedInstanceInfo(),
	}
}

type ec2InstanceTypeInfoProvider struct {
	mu             sync.Mutex
	info           *InstanceInfo
	embeddedLookup map[string]InstanceInfo
}

func (p *ec2InstanceTypeInfoProvider) GetInstanceInfo(ctx context.Context) (*InstanceInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.info != nil {
		return p.info, nil
	}

	instanceType, err := getInstanceTypeFromIMDS(ctx)
	if err != nil {
		return nil, err
	}

	// Try embedded lookup first — no API call needed.
	if info, ok := p.embeddedLookup[instanceType]; ok {
		p.info = &info
		return p.info, nil
	}

	// Fall back to EC2 API for unknown instance types.
	info, err := getInstanceInfoFromEC2API(ctx, instanceType)
	if err != nil {
		return nil, err
	}

	p.info = info
	return p.info, nil
}

func getInstanceTypeFromIMDS(ctx context.Context) (string, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to load AWS config: %w", err)
	}
	imdsClient := imds.NewFromConfig(cfg)
	resp, err := imdsClient.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "instance-type",
	})
	if err != nil {
		return "", fmt.Errorf("failed to get instance type from IMDS: %w", err)
	}
	defer resp.Content.Close()
	body, err := io.ReadAll(resp.Content)
	if err != nil {
		return "", fmt.Errorf("failed to read IMDS response: %w", err)
	}
	return string(body), nil
}

func getInstanceInfoFromEC2API(ctx context.Context, instanceType string) (*InstanceInfo, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	ec2Client := ec2.NewFromConfig(cfg)
	resp, err := ec2Client.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{
		InstanceTypes: []ec2types.InstanceType{ec2types.InstanceType(instanceType)},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe instance type %s: %w", instanceType, err)
	}
	if len(resp.InstanceTypes) == 0 {
		return nil, fmt.Errorf("no results returned for instance type %s", instanceType)
	}

	info := &InstanceInfo{InstanceType: instanceType}

	if gpuInfo := resp.InstanceTypes[0].GpuInfo; gpuInfo != nil {
		for _, gpu := range gpuInfo.Gpus {
			if gpu.Count != nil {
				info.GPUCount += uint(*gpu.Count)
			}
		}
	}

	return info, nil
}

func loadEmbeddedInstanceInfo() map[string]InstanceInfo {
	lookup := make(map[string]InstanceInfo)
	scanner := bufio.NewScanner(bytes.NewReader(instanceInfoData))
	for scanner.Scan() {
		var entry InstanceInfo
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		lookup[entry.InstanceType] = entry
	}
	return lookup
}
