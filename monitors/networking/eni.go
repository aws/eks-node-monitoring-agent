package networking

import (
	"context"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// eniLookupTimeout bounds one IMDS lookup, so an unreachable IMDS can't stall
// the ethtool check.
const eniLookupTimeout = 5 * time.Second

// eniResolver looks up the ENI ID of a network interface on this instance.
type eniResolver interface {
	ENIID(ctx context.Context, interfaceName string) (string, error)
}

type imdsClient interface {
	GetMetadata(ctx context.Context, params *imds.GetMetadataInput, optFns ...func(*imds.Options)) (*imds.GetMetadataOutput, error)
}

type imdsENIResolver struct {
	// client is the IMDS client, built on first use.
	client imdsClient
	// macAddress returns an interface's MAC address. It defaults to reading
	// the host's interface, which NMA shares the network namespace with.
	macAddress func(interfaceName string) (string, error)
	// cache by ifName, assumes distro uses predictable interface names to bound
	// cache size by the number of hardware slots
	byInterface map[string]cachedENI
}

// cachedENI is an interface's resolved ENI ID, and the MAC address it was
// resolved from.
type cachedENI struct {
	macAddress string
	eniID      string
}

func newIMDSENIResolver() *imdsENIResolver {
	return &imdsENIResolver{macAddress: interfaceMACAddress, byInterface: map[string]cachedENI{}}
}

// imds returns the IMDS client, building it on the first lookup that needs
// one. The resolver is only used from the ethtool check's goroutine, so this
// needs no synchronisation.
func (resolver *imdsENIResolver) imds(ctx context.Context) imdsClient {
	if resolver.client == nil {
		resolver.client = newIMDSClient(ctx)
	}
	return resolver.client
}

func interfaceMACAddress(interfaceName string) (string, error) {
	netInterface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return "", err
	}
	if len(netInterface.HardwareAddr) == 0 {
		return "", fmt.Errorf("interface %q has no MAC address", interfaceName)
	}
	return netInterface.HardwareAddr.String(), nil
}

// ENIID returns the AWS Resource ID for the ENI the interface is associated with.
func (resolver *imdsENIResolver) ENIID(ctx context.Context, interfaceName string) (string, error) {
	mac, err := resolver.macAddress(interfaceName)
	if err != nil {
		return "", fmt.Errorf("look up the MAC address of %q: %w", interfaceName, err)
	}
	// only treat an ENI as cached if the MAC address is unchanged for the ifName,
	// handles attachments and detachments
	if cached, ok := resolver.byInterface[interfaceName]; ok && cached.macAddress == mac {
		return cached.eniID, nil
	}
	ctx, cancel := context.WithTimeout(ctx, eniLookupTimeout)
	defer cancel()
	path := "network/interfaces/macs/" + mac + "/interface-id"
	output, err := resolver.imds(ctx).GetMetadata(ctx, &imds.GetMetadataInput{Path: path})
	if err != nil {
		return "", fmt.Errorf("get %s from IMDS: %w", path, err)
	}
	defer output.Content.Close()
	body, err := io.ReadAll(output.Content)
	if err != nil {
		return "", fmt.Errorf("read %s from IMDS: %w", path, err)
	}
	eniID := strings.TrimSpace(string(body))
	if !eniIDPattern.MatchString(eniID) {
		return "", fmt.Errorf("get %s from IMDS: %q is not an ENI ID", path, eniID)
	}
	resolver.byInterface[interfaceName] = cachedENI{macAddress: mac, eniID: eniID}
	return eniID, nil
}

var eniIDPattern = regexp.MustCompile(`^eni-[0-9a-f]+$`)

// newIMDSClient returns an IMDS client configured from the AWS SDK's default
// config, in case variables like AWS_EC2_METADATA_SERVICE_ENDPOINT are set, or
// with the default client settings if that config can't be loaded.
func newIMDSClient(ctx context.Context) *imds.Client {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to load the AWS config for ENI lookups, using IMDS client defaults")
		return imds.New(imds.Options{})
	}
	return imds.NewFromConfig(awsCfg)
}
