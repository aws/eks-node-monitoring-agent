package aws

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

const (
	StageBeta  = "beta"
	StageGamma = "gamma"
	StageProd  = "prod"
)

// betaRegion is the only region with an EKS beta control plane.
const betaRegion = "us-west-2"

// eksEndpointEnvVars are the environment variables, in precedence order, which
// the EKS Addon test harness uses to publish the control-plane endpoint of the
// cluster under test. AWS_ENDPOINT_URL_EKS is the AWS SDK convention,
// EKS_ENDPOINT is what the add-on release test harness injects.
var eksEndpointEnvVars = []string{"AWS_ENDPOINT_URL_EKS", "EKS_ENDPOINT"}

var eksEndpoint string

func init() {
	flag.StringVar(&eksEndpoint, "eks-endpoint", "",
		"EKS control-plane endpoint to use. overrides the endpoint derived from --stage. "+
			"must be in the same region requests are signed for")
}

// GetEksEndpoint resolves the EKS control-plane endpoint for a stage and
// region.
//
// The returned endpoint must belong to the same region the SDK signs requests
// for, otherwise EKS rejects them with a 403 InvalidSignatureException
func GetEksEndpoint(stage, region string) string {
	if endpoint := strings.TrimSpace(eksEndpoint); endpoint != "" {
		return endpoint
	}

	for _, envVar := range eksEndpointEnvVars {
		if endpoint := strings.TrimSpace(os.Getenv(envVar)); endpoint != "" {
			return endpoint
		}
	}

	switch stage {
	case StageBeta:
		// beta only exists in us-west-2. in any other region the caller must
		// supply the endpoint explicitly
		if region == "" || region == betaRegion {
			return fmt.Sprintf("https://api.beta.%s.wesley.amazonaws.com", betaRegion)
		}
		return ""
	case StageGamma:
		return fmt.Sprintf("https://eks.gamma.%s.wesley.amazonaws.com", region)
	}
	return ""
}
