package aws

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
)

func GetIMDSMetadata(path string, imdsClient *imds.Client) ([]byte, error) {
	ctx := context.Background()
	rsp, err := imdsClient.GetMetadata(ctx, &imds.GetMetadataInput{Path: path})
	if err != nil {
		return nil, fmt.Errorf("getting metadata for path %q, %w", path, err)
	}
	bytes, err := io.ReadAll(rsp.Content)
	rsp.Content.Close()
	if err != nil {
		return nil, fmt.Errorf("reading metadata for path %q, %w", path, err)
	}
	return bytes, nil
}
