package networkutils

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// NetworkInterface represents a network interface from networkctl output
type NetworkInterface struct {
	Name     string `json:"Name"`
	LinkFile string `json:"LinkFile"`
}

// NetworkctlOutput represents the JSON output from networkctl command
type NetworkctlOutput struct {
	Interfaces []NetworkInterface `json:"Interfaces"`
}

// Commander runs a command with an enforced execution bound and returns its
// combined output. A non-nil env replaces the child environment.
//
// The bound is the implementation's responsibility: networkctl talks to
// systemd-networkd over dbus and can block indefinitely rather than fail, which
// would otherwise stall the caller for the life of the process.
type Commander interface {
	CombinedOutputEnv(env []string, args ...string) ([]byte, error)
}

// GetNetworkInterfaces runs networkctl and returns parsed network interfaces
func GetNetworkInterfaces(cmder Commander) ([]NetworkInterface, error) {
	// Temporarily unset DBUS_SYSTEM_BUS_ADDRESS to avoid issues with networkctl
	var envWithoutDbus []string
	if os.Getenv("DBUS_SYSTEM_BUS_ADDRESS") != "" {
		envWithoutDbus = make([]string, 0, len(os.Environ()))
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, "DBUS_SYSTEM_BUS_ADDRESS=") {
				envWithoutDbus = append(envWithoutDbus, e)
			}
		}
	}

	output, err := cmder.CombinedOutputEnv(envWithoutDbus, "networkctl", "--json=short")
	if err != nil {
		return nil, fmt.Errorf("networkctl command failed: %w", err)
	}

	var networkctlOutput NetworkctlOutput
	if err := json.Unmarshal(output, &networkctlOutput); err != nil {
		return nil, fmt.Errorf("failed to parse networkctl JSON: %w", err)
	}

	return networkctlOutput.Interfaces, nil
}

// CheckMACAddressPolicy checks the MAC address policy in a configuration file
// Returns nil if the policy is healthy, or an error if it's misconfigured
func CheckMACAddressPolicy(content string) (string, bool) {
	if !strings.Contains(content, "MACAddressPolicy=") {
		return "", true
	}

	lines := strings.Split(content, "\n")
	var currentPolicy string

	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "MACAddressPolicy=") {
			currentPolicy = strings.TrimPrefix(line, "MACAddressPolicy=")
			break
		}
	}

	// An empty string assignment is equivalent to setting "none".
	// per https://www.freedesktop.org/software/systemd/man/latest/systemd.link.html
	isHealthy := currentPolicy == "none" || currentPolicy == ""

	return currentPolicy, isHealthy
}
