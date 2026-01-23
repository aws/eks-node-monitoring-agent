package kernel

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor/resource"
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
	err       error
	obs       *mockObserver
	res       chan monitor.Condition
	observers map[string]*mockObserver
}

func (m *mockManager) Subscribe(rType resource.Type, rParts []resource.Part) (<-chan string, error) {
	if m.err != nil {
		return nil, m.err
	}
	// Create a unique key for this subscription
	key := string(rType)
	for _, part := range rParts {
		key += "-" + string(part)
	}

	// Return existing observer or create new one
	if m.observers == nil {
		m.observers = make(map[string]*mockObserver)
	}
	if obs, exists := m.observers[key]; exists {
		return obs.Subscribe(), nil
	}

	// Create new observer for this subscription
	obs := newMockObserver()
	m.observers[key] = obs
	return obs.Subscribe(), nil
}

func (m *mockManager) Notify(ctx context.Context, condition monitor.Condition) error {
	m.res <- condition
	return nil
}

func TestKernelMonitor(t *testing.T) {
	for _, testCase := range []struct {
		log          string
		reason       string
		resourceType resource.Type
		resourcePart string
		monitor.Severity
	}{
		{"[Mon Jan 1 12:34:56 2022] BUG: something bad happened", "KernelBug", resource.ResourceTypeDmesg, "", monitor.SeverityWarning},
		{"watchdog: BUG: soft lockup - CPU#6 stuck for 23s! [VM Thread:4054]", "SoftLockup", resource.ResourceTypeDmesg, "", monitor.SeverityWarning},
		{`.*fork/exec.*resource temporarily unavailable`, "ForkFailedOutOfPIDs", resource.ResourceTypeJournal, "kubelet", monitor.SeverityFatal},
		{"failed to create new OS thread (foo; errno=11)", "ForkFailedOutOfPIDs", resource.ResourceTypeJournal, "kubelet", monitor.SeverityFatal},
		{"[   32.298491][  T896] kexec[896]: segfault at 0 ip 0000000000000000 sp 00007ffeaf0ff420 error 14 in dash[561ac3c57000+4000]", "AppCrash", resource.ResourceTypeDmesg, "", monitor.SeverityWarning},
		{"task foo:123 blocked for more than 20s", "AppBlocked", resource.ResourceTypeDmesg, "", monitor.SeverityWarning},
		{"nf_conntrack: nf_conntrack: table full, dropping packet", "ConntrackExceededKernel", resource.ResourceTypeDmesg, "", monitor.SeverityWarning},
	} {
		t.Run(testCase.log, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			kernelMonitor := &KernelMonitor{}
			mockManager := mockManager{
				res: make(chan monitor.Condition, 5),
			}
			kernelMonitor.Register(ctx, &mockManager)
			// Give goroutines time to start
			time.Sleep(10 * time.Millisecond)

			// Get the correct observer for this resource type
			key := string(testCase.resourceType)
			if testCase.resourcePart != "" {
				key += "-" + testCase.resourcePart
			}
			obs := mockManager.observers[key]
			if obs == nil {
				t.Fatalf("observer not found for key: %s", key)
			}
			obs.Broadcast("mock", testCase.log)
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
			res: make(chan monitor.Condition, 5),
		}
		mon.Register(ctx, &mockManager)
		// Give goroutines time to start
		time.Sleep(10 * time.Millisecond)

		// Get the cron file observer
		// The key will be "file-/host/var/log/cron.log" or similar
		var cronObs *mockObserver
		for key, obs := range mockManager.observers {
			if strings.Contains(key, "file") && strings.Contains(key, "cron") {
				cronObs = obs
				break
			}
		}
		if cronObs == nil {
			t.Fatal("cron observer not found")
		}

		cronObs.Broadcast("mock", "Sep 17 21:44:01 dev-dsk CROND[13867]: (root) CMD (/usr/hostidentity/bin/hostidentity_generate_aea_jwt.pl)")
		cronObs.Broadcast("mock", "Sep 17 21:44:01 dev-dsk CROND[13867]: (root) CMD (/usr/hostidentity/bin/hostidentity_generate_aea_jwt.pl)")
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
		mockManager := &mockManager{err: mockError}
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
