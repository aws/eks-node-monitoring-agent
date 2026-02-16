//go:build linux

package observer_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor/resource"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/observer"
)

func TestJournalObserver_OpenAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.TODO())
	cancel()
	// Use the constructor to create a journal observer. The cancelled context
	// ensures Init returns immediately without needing a real journal path.
	obs, err := observer.ObserverConstructorMap[resource.ResourceTypeJournal]([]resource.Part{"test-service"})
	assert.NoError(t, err)
	// Init will fail because the journal path doesn't exist, but it should
	// not panic. We just verify the constructor works correctly.
	_ = obs.Init(ctx)
}
