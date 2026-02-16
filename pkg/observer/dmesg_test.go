package observer_test

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor/resource"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/observer"
)

func TestDmesgObserver_Read(t *testing.T) {
	t.Skipf("TODO: this observer currently a wrapper over a fileObserver. not forcing a test with local kmsg data.")

	if _, err := exec.Command("dmesg").Output(); err == os.ErrPermission {
		t.Skipf("skipping the test because dmesg was not callable due to %s", err)
	} else if err != nil {
		t.Fatalf("error testing dmesg: %s", err)
	}

	ctx, cancel := context.WithTimeout(context.TODO(), time.Second)
	defer cancel()

	obs, err := observer.ObserverConstructorMap[resource.ResourceTypeDmesg](nil)
	if err != nil {
		t.Fatal(err)
	}

	obsChan := obs.Subscribe()

	go obs.Init(ctx)

	select {
	case <-obsChan:
		// happy if we get a message.
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
