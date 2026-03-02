package controllers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/collect"
	"github.com/aws/eks-node-monitoring-agent/pkg/logcollection"
	fileutil "github.com/aws/eks-node-monitoring-agent/pkg/util/file"
	netutil "github.com/aws/eks-node-monitoring-agent/pkg/util/net"
)

type nodeDiagnosticController struct {
	kubeClient     client.Client
	nodeName       string
	runtimeContext *config.RuntimeContext
}

func NewNodeDiagnosticController(kubeClient client.Client, nodeName string, runtimeContext *config.RuntimeContext) *nodeDiagnosticController {
	return &nodeDiagnosticController{
		kubeClient:     kubeClient,
		nodeName:       nodeName,
		runtimeContext: runtimeContext,
	}
}

func (c *nodeDiagnosticController) Register(ctx context.Context, m controllerruntime.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("node-diagnostic").
		For(&v1alpha1.NodeDiagnostic{}).
		WithEventFilter(predicate.And(
			// only action the NodeDiagnostic corresponding to the controller node.
			predicate.NewPredicateFuncs(func(object client.Object) bool { return object.GetName() == c.nodeName }),
			// only react to user changes to NodeDiagnostic spec
			predicate.GenerationChangedPredicate{},
			// ignore delete events since they don't trigger an action
			predicate.Funcs{DeleteFunc: func(event.TypedDeleteEvent[client.Object]) bool { return false }},
		)).
		WithOptions(controller.Options{
			// be explicit about concurrency for Reconcile. this allows us to
			// assume that mutating actions are fully completed before procesing
			// the next item.
			MaxConcurrentReconciles: 1,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}

func (c *nodeDiagnosticController) Reconcile(ctx context.Context, nodeDiagnostic *v1alpha1.NodeDiagnostic) (reconcile.Result, error) {
	log := log.FromContext(ctx)

	if nodeDiagnostic.Spec.LogCapture != nil {
		log.Info("updating logCapture status to runnning")
		captureStatus := v1alpha1.CaptureStatus{
			Type: v1alpha1.CaptureTypeLog,
			State: v1alpha1.CaptureState{
				Running: &v1alpha1.CaptureStateRunning{StartedAt: metav1.Now()},
			},
		}

		stored := nodeDiagnostic.DeepCopy()
		nodeDiagnostic.Status.SetCaptureStatus(captureStatus)
		if err := c.kubeClient.Status().Patch(ctx, nodeDiagnostic, client.MergeFrom(stored)); err != nil {
			return reconcile.Result{}, err
		}

		log.Info("beginning log collection")
		archiveReader, issueCount, err := c.collectLogs(ctx, nodeDiagnostic.Spec.Categories)
		if err != nil {
			log.Error(err, "failed to collect logs")
			captureStatus.State = v1alpha1.CaptureState{
				Completed: &v1alpha1.CaptureStateCompleted{
					Reason:     v1alpha1.CaptureStateFailure,
					Message:    "fatal error during log collection process",
					StartedAt:  captureStatus.State.Running.StartedAt,
					FinishedAt: metav1.Now(),
				},
			}
			stored := nodeDiagnostic.DeepCopy()
			nodeDiagnostic.Status.SetCaptureStatus(captureStatus)
			return reconcile.Result{}, c.kubeClient.Status().Patch(ctx, nodeDiagnostic, client.MergeFrom(stored))
		} else {
			log.Info("finished log collection", "issueCount", issueCount)
		}

		log.Info("uploading logs", "url", nodeDiagnostic.Spec.UploadDestination)

		if nodeDiagnostic.Spec.UploadDestination == "node" {
			log.Info("saving logs to /var/log/support for download via node proxy")
			supportDir := filepath.Join(config.HostRoot(), "var/log/support")
			if err := os.MkdirAll(supportDir, 0600); err != nil {
				log.Error(err, "failed to create support directory")
				captureStatus.State = v1alpha1.CaptureState{
					Completed: &v1alpha1.CaptureStateCompleted{
						Reason:     v1alpha1.CaptureStateFailure,
						Message:    "failed to create support directory",
						StartedAt:  captureStatus.State.Running.StartedAt,
						FinishedAt: metav1.Now(),
					},
				}
				stored := nodeDiagnostic.DeepCopy()
				nodeDiagnostic.Status.SetCaptureStatus(captureStatus)
				return reconcile.Result{}, c.kubeClient.Status().Patch(ctx, nodeDiagnostic, client.MergeFrom(stored))
			}
			destPath := filepath.Join(supportDir, fmt.Sprintf("%s-logs.tar.gz", c.nodeName))
			destFile, err := os.Create(destPath)
			if err != nil {
				log.Error(err, "failed to create log file")
				captureStatus.State = v1alpha1.CaptureState{
					Completed: &v1alpha1.CaptureStateCompleted{
						Reason:     v1alpha1.CaptureStateFailure,
						Message:    "failed to create log file",
						StartedAt:  captureStatus.State.Running.StartedAt,
						FinishedAt: metav1.Now(),
					},
				}
				stored := nodeDiagnostic.DeepCopy()
				nodeDiagnostic.Status.SetCaptureStatus(captureStatus)
				return reconcile.Result{}, c.kubeClient.Status().Patch(ctx, nodeDiagnostic, client.MergeFrom(stored))
			}
			defer destFile.Close()
			if _, err := io.Copy(destFile, archiveReader); err != nil {
				log.Error(err, "failed to write logs to file")
				captureStatus.State = v1alpha1.CaptureState{
					Completed: &v1alpha1.CaptureStateCompleted{
						Reason:     v1alpha1.CaptureStateFailure,
						Message:    "failed to write logs to file",
						StartedAt:  captureStatus.State.Running.StartedAt,
						FinishedAt: metav1.Now(),
					},
				}
				stored := nodeDiagnostic.DeepCopy()
				nodeDiagnostic.Status.SetCaptureStatus(captureStatus)
				return reconcile.Result{}, c.kubeClient.Status().Patch(ctx, nodeDiagnostic, client.MergeFrom(stored))
			}
			log.Info("logs saved successfully", "path", destPath)
			captureStatus.State = v1alpha1.CaptureState{
				Completed: &v1alpha1.CaptureStateCompleted{
					Reason:     v1alpha1.CaptureStateSuccess,
					Message:    fmt.Sprintf("successfully saved logs to %s", destPath),
					StartedAt:  captureStatus.State.Running.StartedAt,
					FinishedAt: metav1.Now(),
				},
			}
			if issueCount > 0 {
				captureStatus.State.Completed.Reason = v1alpha1.CaptureStateSuccessWithErrors
				captureStatus.State.Completed.Message = fmt.Sprintf("successfully saved logs to %s with some errors", destPath)
			}
			stored := nodeDiagnostic.DeepCopy()
			nodeDiagnostic.Status.SetCaptureStatus(captureStatus)
			// Delete file from /var/log/support after 10 minutes
			go func() {
				time.Sleep(600 * time.Second)
				if err := os.Remove(destPath); err != nil {
					if !os.IsNotExist(err) {
						log.Error(err, "failed to delete log file after timeout", "path", destPath)
					}
				} else {
					log.Info("successfully deleted log file after timeout", "path", destPath)
				}
			}()
			return reconcile.Result{}, c.kubeClient.Status().Patch(ctx, nodeDiagnostic, client.MergeFrom(stored))
		}
		// wrapping this setup into one function to avoid redundant failure code
		doUpload := func() error {
			uploadRequest, err := http.NewRequestWithContext(ctx, http.MethodPut, string(nodeDiagnostic.Spec.UploadDestination), archiveReader)
			if err != nil {
				return err
			}
			body, err := netutil.DoRequest(uploadRequest)
			if body != nil { // If there's an error back from the API, body is likely nil.
				defer body.Close()
			}
			return err
		}
		// NOTE: max size of an upload via PUT request using S3 REST apis is 5GB. This
		// doesn't sounds like an issue right now, but it could come up in the future.
		// see docs: https://docs.aws.amazon.com/AmazonS3/latest/userguide/upload-objects.html
		if err := doUpload(); err != nil {
			log.Error(err, "failed to upload logs")
			captureStatus.State = v1alpha1.CaptureState{
				Completed: &v1alpha1.CaptureStateCompleted{
					Reason:     v1alpha1.CaptureStateFailure,
					Message:    "fatal error during log upload process",
					StartedAt:  captureStatus.State.Running.StartedAt,
					FinishedAt: metav1.Now(),
				},
			}
			stored := nodeDiagnostic.DeepCopy()
			nodeDiagnostic.Status.SetCaptureStatus(captureStatus)
			return reconcile.Result{}, c.kubeClient.Status().Patch(ctx, nodeDiagnostic, client.MergeFrom(stored))
		}

		log.Info("upload completed successfully")
		captureStatus.State = v1alpha1.CaptureState{
			Completed: &v1alpha1.CaptureStateCompleted{
				Reason:     v1alpha1.CaptureStateSuccess,
				Message:    "successfully uploaded logs with no errors",
				StartedAt:  captureStatus.State.Running.StartedAt,
				FinishedAt: metav1.Now(),
			},
		}

		if issueCount > 0 {
			captureStatus.State.Completed.Reason = v1alpha1.CaptureStateSuccessWithErrors
			captureStatus.State.Completed.Message = "successfully uploaded logs with some errors"
		}

		stored = nodeDiagnostic.DeepCopy()
		nodeDiagnostic.Status.SetCaptureStatus(captureStatus)
		if err := c.kubeClient.Status().Patch(ctx, nodeDiagnostic, client.MergeFrom(stored)); err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

// collectLogs is a small abstraction over the log collector that helps keep the
// controller code brief.
//
// Returns a reader for an archive of the collected logs, a count of how many
// collection tasks failed, and an error.
func (c *nodeDiagnosticController) collectLogs(ctx context.Context, categories []v1alpha1.LogCategory) (io.Reader, int, error) {
	logger := log.FromContext(ctx)

	logDir, err := os.MkdirTemp("", "eks-log-collector-*")
	if err != nil {
		return nil, 0, fmt.Errorf("failed creating temp directory due to: %s", err)
	}
	defer os.RemoveAll(logDir)

	cfg := collect.Config{
		Root:        config.HostRoot(),
		Destination: logDir,
		Tags: append(
			c.runtimeContext.Tags(),
			c.runtimeContext.OSDistro(),
			c.runtimeContext.AcceleratedHardware(),
		),
	}

	acc, err := collect.NewAccessor(cfg)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create accessor: %s", err)
	}

	// if there are any errors in the collection process they will be written to
	// this errors logfile so that users can investigate partial successes.
	const logCaptureReportLog = "log-capture-errors.log"

	subTasksFailed := 0
	var errBuf bytes.Buffer
	for _, collector := range logcollection.GetCollectors(categories...) {
		collectorName := reflect.TypeOf(collector).Elem().Name()
		if err := collector.Collect(acc); err != nil {
			// if there are errors during one step of the collection don't fail,
			// but indicate the failures to show the logs may be incomplete
			logger.Error(err, "failed collection task", "collector", collectorName)
			fmt.Fprintf(&errBuf, "--- Errors in collector %q ---\n%s\n", collectorName, err)
			subTasksFailed += 1
		}
	}
	if errBuf.Len() > 0 {
		if err := os.WriteFile(filepath.Join(logDir, logCaptureReportLog), errBuf.Bytes(), 0644); err != nil {
			return nil, 0, fmt.Errorf("failed to write capture summary: %s", err)
		}
	}

	archiveReader, err := fileutil.TarGzipDir(logDir)
	if err != nil {
		return nil, 0, fmt.Errorf("failed archiving logs due to: %s", err)
	}

	return archiveReader, subTasksFailed, nil
}
