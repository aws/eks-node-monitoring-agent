package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
)

var (
	runtimeCtx     *RuntimeContext
	runtimeCtxLock sync.Mutex
)

type RuntimeContext struct {
	osDistro            string
	acceleratedHardware string
	tags                []string
}

func (rc *RuntimeContext) OSDistro() string {
	runtimeCtxLock.Lock()
	defer runtimeCtxLock.Unlock()
	return rc.osDistro
}

func (rc *RuntimeContext) AcceleratedHardware() string {
	runtimeCtxLock.Lock()
	defer runtimeCtxLock.Unlock()
	return rc.acceleratedHardware
}

func (rc *RuntimeContext) Tags() []string {
	runtimeCtxLock.Lock()
	defer runtimeCtxLock.Unlock()
	return slices.Clone(rc.tags)
}

func (rc *RuntimeContext) AddTags(tags ...string) {
	runtimeCtxLock.Lock()
	defer runtimeCtxLock.Unlock()
	rc.tags = append(rc.tags, tags...)
}

func GetRuntimeContext() *RuntimeContext {
	runtimeCtxLock.Lock()
	defer runtimeCtxLock.Unlock()
	if runtimeCtx == nil {
		var err error
		runtimeCtx, err = generateRuntimeContext()
		if err != nil {
			panic(fmt.Sprintf("failed to generate runtime context: %v", err))
		}
	}
	return runtimeCtx
}

// The NMA is expected to run across on nodes with different operating systems (AL, BR),
// different GPU / Accelerators (Neuron, Nvidia) so therefore derive this information at runtime
// to correctly set up log collection and health monitors.
func generateRuntimeContext() (*RuntimeContext, error) {
	osDistro := "linux"
	if envOSDistro, exists := os.LookupEnv("OSDistro"); exists {
		osDistro = envOSDistro
	}

	acceleratedHardware := "none"
	if hasDeviceType("nvidia") {
		acceleratedHardware = AcceleratedHardwareNvidia
	} else if hasDeviceType("neuron") {
		acceleratedHardware = AcceleratedHardwareNeuron
	}

	// users can populate environment variable 'TAGS' to inject additional tags
	// during runtime to control behavior of context-aware actions.
	tags := strings.FieldsFunc(os.Getenv("TAGS"), func(r rune) bool { return r == ',' })
	// NOTE: failing to find this file should not fail the application.
	releaseInfo, err := os.ReadFile(ToHostPath("/etc/os-release"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// find the operating system 'ID' field and extract the value to be used as a tag.
	if match := regexp.MustCompile(`(?m)^ID=(.*)`).FindSubmatch(releaseInfo); len(match) == 2 {
		osIdentifier := strings.TrimFunc(string(match[1]), func(r rune) bool { return r == '"' })
		tags = append(tags, osIdentifier)
	}
	return &RuntimeContext{
		osDistro:            osDistro,
		acceleratedHardware: acceleratedHardware,
		tags:                tags,
	}, nil
}

func hasDeviceType(deviceName string) bool {
	if devices, err := os.ReadFile(PCIDevicesPath); err == nil {
		return bytes.Contains(devices, []byte(deviceName))
	}
	// We should be able to read devices attached to node. If we can't,
	// the safest thing is to assume that the device isn't present.
	return false
}

// Runtime context tags
const (
	Bottlerocket = "bottlerocket"
	EKSAuto      = "eks-auto"
	Hybrid       = "hybrid"
)

// Accelerated hardware types
const (
	AcceleratedHardwareNvidia = "nvidia"
	AcceleratedHardwareNeuron = "neuron"
)
