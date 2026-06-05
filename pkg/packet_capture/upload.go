package packet_capture

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// uploadFile uploads a file via presigned POST multipart form.
func uploadFile(ctx context.Context, filePath string, uploadConfig *UploadConfig) error {
	logger := log.FromContext(ctx).WithName("packet-capture")

	fileName := filepath.Base(filePath)
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("failed to stat file %s: %w", fileName, err)
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add presigned POST fields
	for key, value := range uploadConfig.Fields {
		fieldValue := value
		if key == "key" {
			fieldValue = strings.ReplaceAll(fieldValue, S3PresignedFilenamePlaceholder, fileName)
		}
		if err := writer.WriteField(key, fieldValue); err != nil {
			return fmt.Errorf("failed to write form field: %w", err)
		}
	}

	// Add the file
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return fmt.Errorf("failed to create form file: %w", err)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", fileName, err)
	}
	defer file.Close()

	if _, err := io.Copy(part, file); err != nil {
		return fmt.Errorf("failed to copy file content: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadConfig.URL, &buf)
	if err != nil {
		return fmt.Errorf("failed to create upload request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upload request failed: %w", err)
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	// SECURITY: Log only file name, file size, and HTTP status code — never URL, fields, or response body
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusCreated {
		logger.Info("upload failed", "fileName", fileName, "fileSize", fileInfo.Size(), "statusCode", resp.StatusCode)
		return fmt.Errorf("upload failed with status %d for file %s", resp.StatusCode, fileName)
	}

	logger.Info("file uploaded", "fileName", fileName, "fileSize", fileInfo.Size(), "statusCode", resp.StatusCode)

	return nil
}
