package validation

import (
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

// ParseURL performs URL validation including hostname validation
func ParseURL(input string) (*url.URL, error) {
	parsedUrl, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %s: %v", input, err)
	}
	_, err = idna.Lookup.ToASCII(parsedUrl.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid hostname %s: %v", parsedUrl.Host, err)
	}
	return parsedUrl, nil
}

// ParseAPIServerURL validates Kubernetes API server URLs
func ParseAPIServerURL(serverURL string) (*url.URL, error) {
	parsedUrl, err := ParseURL(serverURL)
	if err != nil {
		return nil, err
	}
	if parsedUrl.Scheme != "https" {
		return nil, fmt.Errorf("API server URL must use HTTPS scheme")
	}
	return parsedUrl, nil
}

// ParseProviderID validates AWS provider IDs and extracts the instance ID
func ParseProviderID(providerID string) (string, error) {
	parsedUrl, err := ParseURL(providerID)
	if err != nil {
		return "", err
	}
	if parsedUrl.Scheme != "aws" || !strings.HasPrefix(providerID, "aws:///") {
		return "", fmt.Errorf("invalid AWS provider ID format")
	}
	pathParts := strings.Split(parsedUrl.Path, "/")
	if len(pathParts) != 3 {
		return "", fmt.Errorf("invalid AWS provider ID path format")
	}
	instanceID := pathParts[2]
	if !strings.HasPrefix(instanceID, "i-") {
		return "", fmt.Errorf("invalid instance ID format")
	}
	return instanceID, nil
}
