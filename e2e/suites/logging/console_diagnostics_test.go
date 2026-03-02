package logging

import (
	"testing"

	"github.com/aws/eks-node-monitoring-agent/pkg/util/validation"
	"github.com/stretchr/testify/assert"
)

func TestInstanceIDParser(t *testing.T) {
	instanceId, err := validation.ParseProviderID("aws:///eu-west-1a/i-0cb3f1ceeb038fb6c")
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "i-0cb3f1ceeb038fb6c", instanceId)
}
