package logcollection_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.a2z.com/Eks-node-monitoring-agent/api/v1alpha1"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/log_collector/collect"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/logcollection"
)

func TestGetCollectors(t *testing.T) {
	t.Run("HandlesAllCategories", func(t *testing.T) {
		allCollectors := logcollection.GetCollectors(v1alpha1.LogCategoryAll)
		// Build the expected set by fetching each individual category.
		individualCategories := []v1alpha1.LogCategory{
			v1alpha1.LogCategoryBase,
			v1alpha1.LogCategoryDevice,
			v1alpha1.LogCategoryNetworking,
			v1alpha1.LogCategoryRuntime,
			v1alpha1.LogCategorySystem,
		}
		var expected []collect.Collector
		for _, cat := range individualCategories {
			expected = append(expected, logcollection.GetCollectors(cat)...)
		}
		sortCollectors(allCollectors)
		sortCollectors(expected)
		assert.Equal(t, expected, allCollectors)
	})
	t.Run("HandlesMultipleCategories", func(t *testing.T) {
		retrievedCollectors := logcollection.GetCollectors(
			v1alpha1.LogCategoryBase,
			v1alpha1.LogCategoryDevice,
		)
		var expectedCollectors []collect.Collector
		expectedCollectors = append(expectedCollectors, logcollection.GetCollectors(v1alpha1.LogCategoryBase)...)
		expectedCollectors = append(expectedCollectors, logcollection.GetCollectors(v1alpha1.LogCategoryDevice)...)
		sortCollectors(expectedCollectors)
		sortCollectors(retrievedCollectors)
		assert.Equal(t, expectedCollectors, retrievedCollectors)
	})
	t.Run("HandlesSingleCategory", func(t *testing.T) {
		retrievedCollectors := logcollection.GetCollectors(v1alpha1.LogCategoryBase)
		assert.Greater(t, len(retrievedCollectors), 0, "expected at least one collector for Base category")
	})
	t.Run("ReturnsEmptyForEmptyCategories", func(t *testing.T) {
		retrievedCollectors := logcollection.GetCollectors()
		// This is not a case that should be hit in the live path, because the
		// node diagnostic resource defaults to `All` category if not provided.
		// but we are asserting that this is still a safe operation.
		assert.Len(t, retrievedCollectors, 0)
	})
}

// sortCollectors sorts by type name for deterministic comparison.
func sortCollectors(collectors []collect.Collector) {
	sort.Slice(collectors, func(i, j int) bool {
		if nameOrder := strings.Compare(
			reflect.TypeOf(collectors[i]).Elem().String(),
			reflect.TypeOf(collectors[j]).Elem().String(),
		); nameOrder == 0 {
			return reflect.ValueOf(collectors[i]).Pointer() > reflect.ValueOf(collectors[j]).Pointer()
		} else {
			return nameOrder > 0
		}
	})
}
