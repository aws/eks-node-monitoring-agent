package instanceinfo

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
)

//go:embed instance-info.jsonl
var instanceInfoData []byte

// ErrUnknownInstanceType is returned when the instance type is not found in
// the embedded lookup table. Callers can use errors.Is to distinguish this
// from transient failures (e.g. IMDS unavailable).
var ErrUnknownInstanceType = errors.New("unknown instance type")

// InstanceInfo contains hardware metadata for an EC2 instance type.
// Add new fields here as needed — existing JSONL entries without the
// field will unmarshal to the zero value.
type InstanceInfo struct {
	InstanceType   string `json:"instanceType"`
	NvidiaGPUCount uint   `json:"nvidiaGpuCount"`
}

// InstanceTypeInfoProvider returns hardware information about the current EC2 instance type.
type InstanceTypeInfoProvider interface {
	GetInstanceInfo(ctx context.Context) (*InstanceInfo, error)
}

// NewInstanceTypeInfoProvider returns a provider that resolves instance info
// from an embedded lookup table. The result is cached after the first
// successful resolution.
func NewInstanceTypeInfoProvider() *ec2InstanceTypeInfoProvider {
	return &ec2InstanceTypeInfoProvider{
		embeddedLookup: loadEmbeddedInstanceInfo(),
	}
}

type ec2InstanceTypeInfoProvider struct {
	mu             sync.RWMutex
	info           *InstanceInfo
	embeddedLookup map[string]InstanceInfo
}

func (p *ec2InstanceTypeInfoProvider) GetInstanceInfo(ctx context.Context) (*InstanceInfo, error) {
	p.mu.RLock()
	if p.info != nil {
		defer p.mu.RUnlock()
		return p.info, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock.
	if p.info != nil {
		return p.info, nil
	}

	instanceType, err := getInstanceTypeFromIMDS(ctx)
	if err != nil {
		return nil, err
	}

	info, ok := p.embeddedLookup[instanceType]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownInstanceType, instanceType)
	}

	p.info = &info
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
