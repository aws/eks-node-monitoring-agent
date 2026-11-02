package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/sys/mountinfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/pkg/observer"
)

type mockManager struct {
	err       error
	notifyErr error
	obs       observer.BaseObserver
	res       chan monitor.Condition
}

func (m *mockManager) Subscribe(resource.Type, []resource.Part) (<-chan string, error) {
	return m.obs.Subscribe(), m.err
}

func (m *mockManager) Notify(ctx context.Context, condition monitor.Condition) error {
	if m.notifyErr != nil {
		return m.notifyErr
	}
	m.res <- condition
	return nil
}

func TestStorageMonitor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, testCase := range []struct {
		log      string
		severity monitor.Severity
	}{
		{"error mounting \"/var/lib/kubelet/pods/c02d9022-7bc8-4df0-b899-f315edce7112/etc-hosts\" to rootfs at \"/etc/hosts\"", monitor.SeverityWarning},
		{"fs: disk usage and inodes count on following dirs took 9.537338572s:", monitor.SeverityWarning},
	} {
		t.Run(testCase.log, func(t *testing.T) {
			mon := NewStorageMonitor()
			mockManager := mockManager{
				obs: observer.BaseObserver{},
				res: make(chan monitor.Condition, 5),
			}
			mon.Register(ctx, &mockManager)
			mockManager.obs.Broadcast("mock", testCase.log)
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case monitorResult := <-mockManager.res:
				assert.Equal(t, testCase.severity, monitorResult.Severity)
			}
		})
	}

	t.Run("SubscribeError", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := NewStorageMonitor()
		mockError := fmt.Errorf("mock error")
		mockManager := &mockManager{err: mockError, obs: observer.BaseObserver{}}
		actualError := mon.Register(ctx, mockManager)
		assert.EqualError(t, actualError, mockError.Error())
	})
}

func TestPeriodic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	t.Run("XFSSmallAverageClusterSize", func(t *testing.T) {
		mon := NewStorageMonitor()
		mockManager := &mockManager{res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		if err := mon.checkXFS(0); err != nil {
			t.Error(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "XFSSmallAverageClusterSize", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
		}
	})

	t.Run("IODelays", func(t *testing.T) {
		mon := NewStorageMonitor()
		mockManager := &mockManager{res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		var procBytes string
		for range 41 {
			procBytes += "25 " // Will set the first 41 fields to 25 including processID and Name.
		}
		firstProcEntry := procBytes + "1000 " // Set first clock ticks
		if !assert.NoError(t, mon.checkIODelays([]byte(firstProcEntry))) {
			return
		}
		secondProcEntry := procBytes + "2100 " // Set second clock ticks with a 1100 clock tick diff (11 seconds).
		if !assert.NoError(t, mon.checkIODelays([]byte(secondProcEntry))) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "IODelays", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
			assert.Contains(t, monitorResult.Message, "incurred 11.0 seconds of I/O delay")
		}
	})

	t.Run("IODelaysWithNoPreviousBaseline", func(t *testing.T) {
		mon := NewStorageMonitor()
		mockManager := &mockManager{res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		var procBytes string
		for range 42 {
			procBytes += "50000 "
		}
		assert.NoError(t, mon.checkIODelays([]byte(procBytes)))
	})

	t.Run("IODelaysTooSmall", func(t *testing.T) {
		mon := NewStorageMonitor()
		mockManager := &mockManager{res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		var procBytes string
		for range 42 {
			procBytes += "0 "
		}
		assert.NoError(t, mon.checkIODelays([]byte(procBytes)))
	})

	t.Run("IODelaysNotEnoughFields", func(t *testing.T) {
		mon := NewStorageMonitor()
		mockManager := &mockManager{res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		assert.Error(t, mon.checkIODelays([]byte("")))
	})

	t.Run("IODelaysBadNumber", func(t *testing.T) {
		mon := NewStorageMonitor()
		mockManager := &mockManager{res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		var procBytes string
		for range 42 {
			procBytes += "words "
		}
		assert.Error(t, mon.checkIODelays([]byte(procBytes)))
	})
}

func TestBlockDeviceIOErrors(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		shouldDetect   bool
		expectedDevice string
		expectedLoc    string
	}{
		{
			name:           "end_request I/O error on sde",
			logLine:        "[9943662.053217] end_request: I/O error, dev sde, sector 52428288",
			shouldDetect:   true,
			expectedDevice: "sde",
			expectedLoc:    "sector 52428288",
		},
		{
			name:           "Buffer I/O error on md0",
			logLine:        "[9943664.191262] Buffer I/O error on device md0, logical block 209713024",
			shouldDetect:   true,
			expectedDevice: "md0",
			expectedLoc:    "logical block 209713024",
		},
		{
			name:           "blk_update_request I/O error on nvme1n1",
			logLine:        "[12345.678901] blk_update_request: I/O error, dev nvme1n1, sector 1234567",
			shouldDetect:   true,
			expectedDevice: "nvme1n1",
			expectedLoc:    "sector 1234567",
		},
		{
			name:           "blk_update_request I/O error on dm-0",
			logLine:        "[12345.678901] blk_update_request: I/O error, dev dm-0, sector 7654321",
			shouldDetect:   true,
			expectedDevice: "dm-0",
			expectedLoc:    "sector 7654321",
		},
		{
			name:         "Non-I/O error log line",
			logLine:      "[12345.678901] Some other kernel message",
			shouldDetect: false,
		},
		{
			name:         "Empty log line",
			logLine:      "",
			shouldDetect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockManager := &mockManager{res: make(chan monitor.Condition, 1)}
			mon := NewStorageMonitor()
			mon.manager = mockManager

			require.NoError(t, mon.handleBlockDeviceIOErrors(tt.logLine))

			if tt.shouldDetect {
				select {
				case monitorResult := <-mockManager.res:
					require.Equal(t, "BlockDeviceIOError", monitorResult.Reason)
					require.Contains(t, monitorResult.Message, tt.expectedDevice)
					require.Contains(t, monitorResult.Message, tt.expectedLoc)
				default:
					require.Fail(t, "expected a condition to be emitted, but none was emitted")
				}
			} else {
				select {
				case <-mockManager.res:
					require.Fail(t, "expected no condition to be emitted, but one was emitted")
				default:
				}
			}
		})
	}
}

func TestSelectPartitions(t *testing.T) {
	for _, tt := range []struct {
		name     string
		fixture  string
		hostRoot string
		kubelet  deviceNumber
		want     []partition
	}{
		{
			name:     "DaemonSet on AL2023, kubelet on a RAID0 of instance store disks",
			fixture:  "mountinfo-al2023-daemonset-raid0.txt",
			hostRoot: "/host",
			kubelet:  deviceNumber{9, 127},
			want: []partition{
				{device: deviceNumber{259, 2}, source: "/dev/nvme0n1p1", path: "/host", mountpoints: []string{"/"}},
				{device: deviceNumber{259, 4}, source: "/dev/nvme0n1p128", path: "/host/boot/efi", mountpoints: []string{"/boot/efi"}},
			},
		},
		{
			name:     "DaemonSet on Bottlerocket, kubelet on ephemeral storage",
			fixture:  "mountinfo-bottlerocket-daemonset-ephemeral.txt",
			hostRoot: "/host",
			kubelet:  deviceNumber{259, 1},
			want: []partition{
				{device: deviceNumber{259, 14}, source: "/dev/nvme0n1p12", path: "/host/.bottlerocket", mountpoints: []string{"/.bottlerocket", "/var/lib/bottlerocket"}},
				{device: deviceNumber{259, 17}, source: "/dev/nvme1n1p1", path: "/host/local", mountpoints: []string{"/local", "/mnt", "/opt", "/var"}},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", tt.fixture))
			require.NoError(t, err)
			defer f.Close()
			mounts, err := mountinfo.GetMountsFromReader(f, nil)
			require.NoError(t, err)
			assert.Equal(t, tt.want, selectPartitions(mounts, tt.hostRoot, map[deviceNumber]bool{tt.kubelet: true}))
		})
	}

	t.Run("pod volumes", func(t *testing.T) {
		mounts, err := mountinfo.GetMountsFromReader(strings.NewReader(
			"30 25 259:3 / /var/lib/kubelet/plugins/kubernetes.io/csi/ebs.csi.aws.com/0a1b/globalmount rw,relatime - ext4 /dev/nvme3n1 rw\n"+
				"31 25 259:3 / /var/lib/kubelet/pods/0a1b/volumes/kubernetes.io~csi/pvc-0a1b/mount rw,relatime - ext4 /dev/nvme3n1 rw\n"+
				"32 25 259:4 / /var/log rw,relatime - xfs /dev/nvme4n1 rw\n",
		), nil)
		require.NoError(t, err)
		assert.Equal(t, []partition{
			{device: deviceNumber{259, 4}, source: "/dev/nvme4n1", path: "/var/log", mountpoints: []string{"/var/log"}},
		}, selectPartitions(mounts, "/", nil))
	})
}

func TestCheckPartition(t *testing.T) {
	mgr := &mockManager{res: make(chan monitor.Condition, 10)}
	mon := NewStorageMonitor()
	mon.manager = mgr
	now := time.Unix(0, 0)
	mon.now = func() time.Time { return now }
	var spaceUsed, inodesUsed float64
	var usageErr error
	mon.partitionUsage = func(string) (float64, float64, error) { return spaceUsed, inodesUsed, usageErr }
	p := partition{device: deviceNumber{259, 12}, source: "/dev/nvme1n1p1", path: "/local", mountpoints: []string{"/local", "/var"}}

	check := func(space, inodes float64) []string {
		t.Helper()
		spaceUsed, inodesUsed = space, inodes
		require.NoError(t, mon.checkPartition(p))
		var messages []string
		for {
			select {
			case c := <-mgr.res:
				assert.Equal(t, "PartitionNearlyFull", c.Reason)
				assert.Equal(t, monitor.SeverityWarning, c.Severity)
				messages = append(messages, c.Message)
			default:
				return messages
			}
		}
	}

	assert.Empty(t, check(0.90, 0.95), "at the thresholds")
	assert.Equal(t, []string{"/dev/nvme1n1p1 mounted at /local, /var has 8.0% of its space free"}, check(0.92, 0.5))
	assert.Empty(t, check(0.93, 0.5), "unchanged within the re-notify interval")

	now = now.Add(partitionWarningReNotifyInterval)
	assert.Len(t, check(0.93, 0.5), 1, "unchanged after the re-notify interval")
	assert.Equal(t, []string{"/dev/nvme1n1p1 mounted at /local, /var has 7.0% of its space and 3.0% of its inodes free"}, check(0.93, 0.97), "inodes crossed as well")
	assert.Equal(t, []string{"/dev/nvme1n1p1 mounted at /local, /var has 3.0% of its inodes free"}, check(0.5, 0.97), "space recovered")

	assert.Empty(t, check(0.5, 0.5), "recovered")
	assert.Len(t, check(0.92, 0.5), 1, "crossed again after recovering")

	assert.Empty(t, check(0.5, 0.5), "recovered")
	mgr.notifyErr = errors.New("notify failed")
	spaceUsed = 0.92
	assert.Error(t, mon.checkPartition(p))
	mgr.notifyErr = nil
	assert.Len(t, check(0.92, 0.5), 1, "retried after a failed notification")

	usageErr = errors.New("statfs failed")
	assert.Error(t, mon.checkPartition(p))
}
