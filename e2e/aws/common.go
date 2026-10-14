package aws

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

const (
	StageBeta = "beta"
	StageProd = "prod"
)

var eksEndpointEnvVars = []string{
	"AWS_ENDPOINT_URL_EKS",
	"EKS_ENDPOINT",
	"AWS_ENDPOINT_URL",
}

var eksEndpoint string

func init() {
	flag.StringVar(&eksEndpoint, "eks-endpoint", "",
		"EKS control-plane endpoint to use. defaults to the endpoint for the configured region. "+
			"may also be supplied via "+strings.Join(eksEndpointEnvVars, ", "))
}

// GetEksEndpoint returns the EKS control-plane endpoint the suite should use. An
// empty result means "use the SDK's default endpoint for the configured
// region", which is what prod wants.
func GetEksEndpoint() string {
	if endpoint := strings.TrimSpace(eksEndpoint); endpoint != "" {
		return endpoint
	}
	for _, envVar := range eksEndpointEnvVars {
		if endpoint := strings.TrimSpace(os.Getenv(envVar)); endpoint != "" {
			return endpoint
		}
	}
	return ""
}

// CheckStageEndpoint reports a non-prod stage that was given no endpoint to
// talk to. It is advisory rather than fatal.
func CheckStageEndpoint(stage string) error {
	if stage == StageProd || GetEksEndpoint() != "" {
		return nil
	}
	return fmt.Errorf(
		"--stage=%s was given no EKS endpoint, falling back to the default endpoint "+
			"for the configured region. pass --eks-endpoint or set one of %s to target "+
			"another control plane",
		stage, strings.Join(eksEndpointEnvVars, ", "),
	)
}
