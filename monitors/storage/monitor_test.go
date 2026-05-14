package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
	"github.com/aws/eks-node-monitoring-agent/pkg/observer"
)

type mockManager struct {
	err error
	obs observer.BaseObserver
	res chan monitor.Condition
}

func (m *mockManager) Subscribe(resource.Type, []resource.Part) (<-chan string, error) {
	return m.obs.Subscribe(), m.err
}

func (m *mockManager) Notify(ctx context.Context, condition monitor.Condition) error {
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
			var matched bool
			var device, location string

			if matches := ioErrorEndRequest.FindStringSubmatch(tt.logLine); len(matches) > 2 {
				matched = true
				device = matches[1]
				location = "sector " + matches[2]
			} else if matches := ioErrorBufferIO.FindStringSubmatch(tt.logLine); len(matches) > 2 {
				matched = true
				device = matches[1]
				location = "logical block " + matches[2]
			} else if matches := ioErrorBlkUpdate.FindStringSubmatch(tt.logLine); len(matches) > 2 {
				matched = true
				device = matches[1]
				location = "sector " + matches[2]
			}

			assert.Equal(t, tt.shouldDetect, matched)
			if tt.shouldDetect {
				assert.Equal(t, tt.expectedDevice, device)
				assert.Equal(t, tt.expectedLoc, location)
			}
		})
	}
}
