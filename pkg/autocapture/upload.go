package autocapture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

const (
	// NMA has a 200Mi memory limit and a multipart upload buffers PartSize x
	// (Concurrency+1), so one 8 MiB part at a time keeps that small. The
	// default 16 MiB threshold would buffer more before the first part.
	uploadPartSize    = 8 << 20
	uploadConcurrency = 1

	// For multipart uploads (steps over 8 MiB): after the capture timer fires the
	// caller's context is cancelled, so the abort needs a fresh one, or unfinished
	// parts stay in S3 and are billed.
	uploadFailTimeout = 30 * time.Second
)

// objectUploader is what the worker needs from S3, so tests never touch it.
type objectUploader interface {
	Upload(ctx context.Context, bucket, key string, body io.Reader) error
}

type s3Uploader struct {
	client *transfermanager.Client
}

// newS3Uploader uses the SDK default credential chain, so the node role works
// with no agent change. A bucket in another region than the node's is not
// supported.
func newS3Uploader(ctx context.Context) (objectUploader, error) {
	// The agent pod sets no AWS_REGION, so fall back to the node's own region.
	cfg, err := config.LoadDefaultConfig(ctx, config.WithEC2IMDSRegion())
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	client := transfermanager.New(s3.NewFromConfig(cfg), func(o *transfermanager.Options) {
		o.Concurrency = uploadConcurrency
		o.PartSizeBytes = uploadPartSize
		o.MultipartUploadThreshold = uploadPartSize
		o.FailTimeout = uploadFailTimeout
	})
	return &s3Uploader{client: client}, nil
}

func (u *s3Uploader) Upload(ctx context.Context, bucket, key string, body io.Reader) error {
	_, err := u.client.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   body,
	})
	return err
}

// stepKey groups one capture's steps under one prefix. The "/" in a step name
// is replaced so it does not add a folder level.
func stepKey(node string, at time.Time, condition, stepName string) string {
	return fmt.Sprintf("node-captures/%s/%s-%s/%s.tar.gz",
		node, at.UTC().Format("20060102T150405Z"), condition, strings.ReplaceAll(stepName, "/", "."))
}

func isAccessDenied(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "AccessDenied"
}
