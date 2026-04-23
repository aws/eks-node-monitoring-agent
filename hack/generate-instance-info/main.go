// Command generate-instance-info queries EC2 DescribeInstanceTypes across all
// opted-in regions and produces a JSONL file of instance type metadata. The
// output is written to internal/pkg/instanceinfo/instance-info.jsonl and is
// intended to be run periodically (e.g. via GitHub Actions) to discover new
// instance types.
//
// To add new fields: add the field to the instanceInfo struct, populate it
// from the EC2 response in collectInstanceInfo(), and update the InstanceInfo
// struct in internal/pkg/instanceinfo/provider.go to match. Zero-value fields
// are omitted from the JSONL output.
//
// Usage:
//
//	go run ./hack/generate-instance-info/...
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// instanceInfo mirrors internal/pkg/instanceinfo.InstanceInfo.
// Add new fields here and in collectInstanceInfo() to extend the
// generated data. Fields with zero values are omitted from the output.
// Only entries where at least one field beyond InstanceType is non-zero
// are written to the JSONL.
type instanceInfo struct {
	InstanceType  string `json:"instanceType"`
	NvidiaGPUCount uint  `json:"nvidiaGpuCount,omitempty"`
}

const outputPath = "internal/pkg/instanceinfo/instance-info.jsonl"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	regions, err := getOptedInRegions(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to get regions: %w", err)
	}

	fmt.Fprintf(os.Stderr, "scanning %d regions for instance types...\n", len(regions))

	infoByType := make(map[string]instanceInfo)
	for _, region := range regions {
		regionalCfg := cfg.Copy()
		regionalCfg.Region = region

		if err := addInstanceTypes(ctx, regionalCfg, infoByType); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to scan region %s: %v\n", region, err)
			continue
		}
	}

	// Filter to only entries that have meaningful data beyond the instance type name.
	var entries []instanceInfo
	empty := instanceInfo{}
	for _, info := range infoByType {
		// Include if any field besides InstanceType is non-zero.
		check := info
		check.InstanceType = ""
		if check != empty {
			entries = append(entries, info)
		}
	}

	// Sort by instance type for stable output.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].InstanceType < entries[j].InstanceType
	})

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			return fmt.Errorf("failed to encode entry: %w", err)
		}
	}

	fmt.Fprintf(os.Stderr, "wrote %d instance types to %s\n", len(entries), outputPath)
	return nil
}

// collectInstanceInfo extracts relevant fields from an EC2 InstanceTypeInfo.
// Extend this function when adding new fields to instanceInfo.
func collectInstanceInfo(it ec2types.InstanceTypeInfo) instanceInfo {
	info := instanceInfo{
		InstanceType: string(it.InstanceType),
	}

	if it.GpuInfo != nil {
		for _, gpu := range it.GpuInfo.Gpus {
			if gpu.Count != nil && gpu.Manufacturer != nil && *gpu.Manufacturer == "NVIDIA" {
				info.NvidiaGPUCount += uint(*gpu.Count)
			}
		}
	}

	return info
}

func getOptedInRegions(ctx context.Context, cfg aws.Config) ([]string, error) {
	client := ec2.NewFromConfig(cfg)
	resp, err := client.DescribeRegions(ctx, &ec2.DescribeRegionsInput{
		AllRegions: aws.Bool(false),
	})
	if err != nil {
		return nil, err
	}

	var regions []string
	for _, r := range resp.Regions {
		if r.RegionName != nil {
			regions = append(regions, *r.RegionName)
		}
	}
	sort.Strings(regions)
	return regions, nil
}

func addInstanceTypes(ctx context.Context, cfg aws.Config, results map[string]instanceInfo) error {
	client := ec2.NewFromConfig(cfg)
	paginator := ec2.NewDescribeInstanceTypesPaginator(client, &ec2.DescribeInstanceTypesInput{})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}

		for _, it := range page.InstanceTypes {
			instanceType := string(it.InstanceType)
			if _, exists := results[instanceType]; exists {
				continue
			}
			results[instanceType] = collectInstanceInfo(it)
		}
	}
	return nil
}
