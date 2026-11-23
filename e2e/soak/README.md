# NMA soak test

A long-running soak and memory-leak gate for the node monitoring agent. A
controller runs continuously on a dedicated soak cluster. It keeps a steady
background load and a stream of churn pods on the nodes, samples every
agent's memory and resource usage, and evaluates each requested release for
leaks.

## Packages

| Package | Purpose |
|---|---|
| `workload` | Background workload (1000 pods, `manifests/background.yaml`) and shared churn objects (`manifests/churn.yaml`), plus the churn pod templates (`manifests/churn-pods.yaml`) |
| `churn` | Creates churn pods at 120/min, deletes each after 60s, caps in-flight pods at 200 |
| `collector` | Samples each agent pod: Go heap and runtime memory, process RSS, goroutines and open FDs from `:8003/metrics`; container working set and anonymous RSS (`container_memory_rss`) from cAdvisor; restarts and OOM kills from pod status |
| `publish` | Publishes samples, churn stats, window results and a heartbeat to CloudWatch namespace `EKSNodeMonitoringAgent/Soak` |
| `analysis` | Leak checks (peaks; slope and median growth of Go heap, Go runtime memory, anonymous RSS and working set; goroutine/FD growth; restarts and OOM kills during the window) against `analysis/thresholds.yaml`. Process RSS is collected but not gated, since most of it is file-backed. |
| `controller` | Release state machine driven by SSM |
| `cmd/soak-controller` | The controller binary (`make build-soak-controller`) |

## Cluster contract

The soak cluster must provide:

- Nodes labelled `nma-soak/pool=soak` for the background workload, with kubelet
  `maxPods` of about 110 so each holds about 100 workload pods.
- Nodes labelled `nma-soak/pool=churn` and tainted `nma-soak/pool=churn:NoSchedule`
  for churn pods, with `maxPods` of about 250.
- The EFS CSI driver, an EFS filesystem (passed as `EFS_FILESYSTEM_ID`), and
  the VPC CNI with network policy enabled.
- The agent DaemonSet `kube-system/eks-node-monitoring-agent` serving metrics
  on `:8003`.

The controller's service account needs:

- get/list on pods and nodes, and get on `pods/proxy` and `nodes/proxy`;
- get on the agent DaemonSet;
- create/delete pods and server-side apply of the workload objects in the
  soak namespace;
- AWS: `ssm:GetParameter` on the request parameter, `ssm:PutParameter` on the
  result parameter, and `cloudwatch:PutMetricData` for the soak namespace.

## Configuration

Environment variables (see `cmd/soak-controller/main.go`):

| Variable | Default |
|---|---|
| `STAGE` | required |
| `EFS_FILESYSTEM_ID` | required |
| `SOAK_NAMESPACE` | `nma-soak` |
| `SSM_REQUEST_PARAMETER` | `/nma-soak/<STAGE>/request` |
| `SSM_RESULT_PARAMETER` | `/nma-soak/<STAGE>/result` |
| `SOAK_WINDOW` | `5h` (includes the 30m warm-up) |
| `ROLLOUT_TIMEOUT` | `30m` |
| `THRESHOLDS_FILE` | built-in `analysis/thresholds.yaml` |

## Release flow

1. The release pipeline writes the request parameter:

   ```json
   {"release_id": "...", "release_version": "v1.7.4-eksbuild.1", "image_uri": "...", "chart_version": "1.7.4"}
   ```

2. Within a minute the controller writes the result parameter with status
   `ROLLING_OUT`, and waits until the agent DaemonSet runs `image_uri` and
   every pod is available. If that takes longer than `ROLLOUT_TIMEOUT`, the
   status becomes `ROLLOUT_TIMEOUT` with verdict `FAIL`.
3. It then evaluates a `SOAK_WINDOW` window (status `EVALUATING`) and writes
   status `COMPLETE` with the verdict and analysis.
4. The pipeline should pass the release only when the result's
   `release_id` matches, the status is `COMPLETE`, and the verdict is `PASS`
   or `RECORD_ONLY`.

A newer request before a window finishes marks the older release
`SUPERSEDED`. A release that already has a final result is never evaluated
again. Samples are only held in memory, so a controller restart starts the
current release's window over.

Between releases the controller evaluates rolling windows on the running
build and publishes their results, so leaks and a broken soak still alarm.

## Thresholds

`analysis/thresholds.yaml` holds placeholder thresholds with
`recordOnly: true`. While record-only, failed checks are reported (in the
result and the `LeakDetected`/`ThresholdBreach` metrics) but the verdict is
`RECORD_ONLY`, which doesn't block a release. After the calibration runs, set
each peak to the maximum observed peak x 1.15, keep memory peaks at or below
80% of the chart's memory limit (enforced by a unit test), and set
`recordOnly: false`.
