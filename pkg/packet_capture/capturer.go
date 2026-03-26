package packet_capture

import (
	"context"
	"time"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
)

const (
	DefaultRotationSizeMB = 10
	FileCheckInterval     = 5 * time.Second
	DiskUsageThreshold    = 0.90
	MaxRetries            = 1

	// MaxRotatedFiles is the upper bound on the number of rotated capture files
	// tcpdump will keep before wrapping. Set high to effectively disable wrapping
	// since we upload and delete files as they are produced.
	MaxRotatedFiles = "10000"

	// CompressCmd is the post-rotation command tcpdump runs on each completed
	// capture file via -z. igzip is a fast gzip-compatible compressor available
	// on the Bottlerocket AMI.
	CompressCmd = "igzip"

	// S3PresignedFilenamePlaceholder is the placeholder used in S3 presigned POST
	// key fields. S3 replaces "${filename}" with the actual uploaded file name at
	// upload time. See: https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-HTTPPOSTForms.html
	S3PresignedFilenamePlaceholder = "${filename}"
)

// Capturer is the interface for packet capture implementations.
type Capturer interface {
	CaptureAndUpload(ctx context.Context, config Config) ([]error, error)
}

// Config holds the configuration for a packet capture operation.
type Config struct {
	OutputPath   string
	Spec         *v1alpha1.PacketCapture
	UploadConfig *UploadConfig
}

// UploadConfig holds the presigned POST upload configuration.
type UploadConfig struct {
	URL    string
	Fields map[string]string
}
