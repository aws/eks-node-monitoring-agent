package kernel

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"golang.a2z.com/Eks-node-monitoring-agent/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/monitor/resource"
)

// mockObserver provides a simple channel-based observer for testing
type mockObserver struct {
	ch chan string
}

func newMockObserver() *mockObserver {
	return &mockObserver{
		ch: make(chan string, 100),
	}
}

func (m *mockObserver) Subscribe() <-chan string {
	return m.ch
}

func (m *mockObserver) Broadcast(source, message string) {
	m.ch <- message
}

type mockManager struct {
	err error
	obs *mockObserver
	res chan monitor.Condition
}

func (m *mockManager) Subscribe(resource.Type, []resource.Part) (<-chan string, error) {
	return m.obs.Subscribe(), m.err
}

func (m *mockManager) Notify(ctx context.Context, condition monitor.Condition) error {
	m.res <- condition
	return nil
}

func TestKernelMonitor(t *testing.T) {
	for _, testCase := range []struct {
		log    string
		reason string
		monitor.Severity
	}{
		{"[Mon Jan 1 12:34:56 2022] BUG: something bad happened", "KernelBug", monitor.SeverityWarning},
		{"watchdog: BUG: soft lockup - CPU#6 stuck for 23s! [VM Thread:4054]", "SoftLockup", monitor.SeverityWarning},
		{`.*fork/exec.*resource temporarily unavailable`, "ForkFailedOutOfPIDs", monitor.SeverityFatal},
		{"failed to create new OS thread (foo; errno=11)", "ForkFailedOutOfPIDs", monitor.SeverityFatal},
		{"[   32.298491][  T896] kexec[896]: segfault at 0 ip 0000000000000000 sp 00007ffeaf0ff420 error 14 in dash[561ac3c57000+4000]", "AppCrash", monitor.SeverityWarning},
		{"task foo:123 blocked for more than 20s", "AppBlocked", monitor.SeverityWarning},
		{"nf_conntrack: nf_conntrack: table full, dropping packet", "ConntrackExceededKernel", monitor.SeverityWarning},
	} {
		t.Run(testCase.log, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			kernelMonitor := &KernelMonitor{}
			mockManager := mockManager{
				obs: newMockObserver(),
				res: make(chan monitor.Condition, 5),
			}
			kernelMonitor.Register(ctx, &mockManager)
			mockManager.obs.Broadcast("mock", testCase.log)
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case monitorResult := <-mockManager.res:
				assert.Equal(t, testCase.reason, monitorResult.Reason)
				assert.Equal(t, testCase.Severity, monitorResult.Severity)
			}
		})
	}

	t.Run("RapidCron", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := &KernelMonitor{}
		mockManager := mockManager{
			obs: newMockObserver(),
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, &mockManager)
		mockManager.obs.Broadcast("mock", "Sep 17 21:44:01 dev-dsk CROND[13867]: (root) CMD (/usr/hostidentity/bin/hostidentity_generate_aea_jwt.pl)")
		mockManager.obs.Broadcast("mock", "Sep 17 21:44:01 dev-dsk CROND[13867]: (root) CMD (/usr/hostidentity/bin/hostidentity_generate_aea_jwt.pl)")
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, "RapidCron", monitorResult.Reason)
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
		}
	})

	t.Run("SubscribeError", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := &KernelMonitor{}
		mockError := fmt.Errorf("mock error")
		mockManager := &mockManager{err: mockError, obs: newMockObserver()}
		actualError := mon.Register(ctx, mockManager)
		assert.EqualError(t, actualError, mockError.Error())
	})
}

func TestKernelPeriodic(t *testing.T) {
	t.Run("MaxOpenFilesNoop", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := &KernelMonitor{}
		mockManager := &mockManager{obs: newMockObserver(), res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		if err := mon.checkOpenedFiles(1, 10); err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, 0, len(mockManager.res))
	})

	t.Run("MaxOpenFilesWarning", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := &KernelMonitor{}
		mockManager := &mockManager{obs: newMockObserver(), res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		if err := mon.checkOpenedFiles(8, 10); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
			assert.Equal(t, "ApproachingMaxOpenFiles", monitorResult.Reason)
		}
	})

	t.Run("KernelPidMaxNoop", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := &KernelMonitor{}
		mockManager := &mockManager{obs: newMockObserver(), res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		if err := mon.checkPids(0, 10); err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, 0, len(mockManager.res))
	})

	t.Run("KernelPidMaxWarning", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := &KernelMonitor{}
		mockManager := &mockManager{obs: newMockObserver(), res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		if err := mon.checkPids(8, 10); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
			assert.Equal(t, "ApproachingKernelPidMax", monitorResult.Reason)
		}
	})

	t.Run("ExcessiveZombieProcessesNoop", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := &KernelMonitor{}
		mockManager := &mockManager{obs: newMockObserver(), res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		if err := mon.checkZombies(0); err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, 0, len(mockManager.res))
	})

	t.Run("ExcessiveZombieProcesses", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := &KernelMonitor{}
		mockManager := &mockManager{obs: newMockObserver(), res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		if err := mon.checkZombies(20); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
			assert.Equal(t, "ExcessiveZombieProcesses", monitorResult.Reason)
		}
	})

	t.Run("LargeEnvironment", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := &KernelMonitor{}
		mockManager := &mockManager{obs: newMockObserver(), res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		var envs []byte
		for i := range 1001 {
			envs = append(envs, []byte(fmt.Sprintf("%d\x00", i))...)
		}
		if err := mon.checkEnvironment(envs, 0); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case monitorResult := <-mockManager.res:
			assert.Equal(t, monitor.SeverityWarning, monitorResult.Severity)
			assert.Equal(t, "LargeEnvironment", monitorResult.Reason)
		}
	})

	t.Run("LargeEnvironmentNull", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mon := &KernelMonitor{}
		mockManager := &mockManager{obs: newMockObserver(), res: make(chan monitor.Condition, 5)}
		mon.Register(ctx, mockManager)
		var envs []byte
		for range 5000 {
			envs = append(envs, 0)
		}
		if err := mon.checkEnvironment(envs, 0); err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, 0, len(mockManager.res))
	})
}
