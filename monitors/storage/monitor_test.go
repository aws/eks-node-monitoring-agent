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
