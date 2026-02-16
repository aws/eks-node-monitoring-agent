package observer_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"golang.a2z.com/Eks-node-monitoring-agent/pkg/observer"
)

func TestObserver_SubscriptionChannel(t *testing.T) {
	const expectedMessage = "test"

	obs := observer.BaseObserver{}
	obsChan := obs.Subscribe()
	obs.Broadcast("mock", expectedMessage)
	select {
	case actualMessage := <-obsChan:
		assert.Equal(t, expectedMessage, actualMessage)
	default:
		t.Fatal("did not receive message from observer channel")
	}
}
