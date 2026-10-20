package autocapture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/aws/eks-node-monitoring-agent/api/v1alpha1"
	"github.com/aws/eks-node-monitoring-agent/pkg/conditions"
	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/log_collector/collect"
	"github.com/aws/eks-node-monitoring-agent/pkg/logcollection"
)

// Step names come from the collector's type name, so each fake gets its own type.
type fakeCollector struct{ fn func(*collect.Accessor) error }

func (f *fakeCollector) Collect(acc *collect.Accessor) error {
	if f.fn == nil {
		return nil
	}
	return f.fn(acc)
}

type (
	alpha   struct{ fakeCollector }
	bravo   struct{ fakeCollector }
	charlie struct{ fakeCollector }
	delta   struct{ fakeCollector }
)

// A round total makes the percentage limit an exact number of bytes.
const (
	testTotal = 100 << 30
	testLimit = testTotal * minFreePercent / 100
)

var baseAndNetworking = []v1alpha1.LogCategory{v1alpha1.LogCategoryBase, v1alpha1.LogCategoryNetworking}

// delta is in both categories, like the real collectors that are shared.
func fixture() map[v1alpha1.LogCategory][]collect.Collector {
	return map[v1alpha1.LogCategory][]collect.Collector{
		v1alpha1.LogCategoryBase:       {&alpha{}, &bravo{}, &delta{}},
		v1alpha1.LogCategoryNetworking: {&charlie{}, &delta{}},
	}
}

func fakeGet(byCategory map[v1alpha1.LogCategory][]collect.Collector) func(...v1alpha1.LogCategory) []collect.Collector {
	return func(categories ...v1alpha1.LogCategory) (out []collect.Collector) {
		for _, category := range categories {
			out = append(out, byCategory[category]...)
		}
		return out
	}
}

// testDeps fakes the host and points TMPDIR at an empty folder, so tests can
// check that no step folder is left behind.
func testDeps(t *testing.T, byCategory map[v1alpha1.LogCategory][]collect.Collector) (collectDeps, string) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(tmp, "none")) // keep NewAccessor off the developer's AWS config
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(tmp, "none"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	return collectDeps{
		freeDisk:   func(string) (uint64, uint64, error) { return testTotal, testTotal, nil },
		collectors: fakeGet(byCategory),
	}, tmp
}

func runSteps(ctx context.Context, deps collectDeps, handle StepFunc) (Summary, error) {
	return collectSteps(ctx, &config.RuntimeContext{}, baseAndNetworking, handle, deps)
}

func requireEmptyDir(t *testing.T, dir string) {
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "no step folder may be left behind")
}

func orderedNames(get func(...v1alpha1.LogCategory) []collect.Collector, categories []v1alpha1.LogCategory) (out []string) {
	for _, c := range orderedCollectors(get, categories) {
		out = append(out, c.name)
	}
	return out
}

func TestOrderedCollectors_FollowsCategoryOrder(t *testing.T) {
	// Equal to this list also proves the names are unique.
	want := []string{"Base/alpha", "Base/bravo", "Base/delta", "Networking/charlie", "Networking/delta"}
	assert.Equal(t, want, orderedNames(fakeGet(fixture()), baseAndNetworking))
}

// Only the real registry has the map iteration that makes GetCollectors random.
func TestOrderedCollectors_RealRegistryIsStable(t *testing.T) {
	categories := CategoriesFor(conditions.NetworkingReady)
	first := orderedNames(logcollection.GetCollectors, categories)
	for i := 0; i < 100; i++ {
		require.Equal(t, first, orderedNames(logcollection.GetCollectors, categories), "run %d", i)
	}
}

func TestCollectSteps_EachStepGetsOwnFolderRemovedAfterHandle(t *testing.T) {
	for _, tc := range []struct {
		name      string
		handleErr error
	}{
		{name: "handle succeeds"},
		{name: "handle fails and loop continues", handleErr: errors.New("handle boom")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, tmp := testDeps(t, fixture())

			var dirs []string
			summary, err := runSteps(context.Background(), deps, func(_ context.Context, step Step) error {
				assert.DirExists(t, step.Dir, "the folder must exist while handle runs")
				assert.NotContains(t, dirs, step.Dir, "each step needs its own folder")
				dirs = append(dirs, step.Dir)
				return tc.handleErr
			})

			require.NoError(t, err)
			assert.Len(t, dirs, 5)
			for _, dir := range dirs {
				assert.NoDirExists(t, dir, "the folder must be gone after handle returns")
			}
			requireEmptyDir(t, tmp)
			assert.Equal(t, 5, summary.Steps)
			if tc.handleErr != nil {
				assert.Equal(t, 5, summary.HandleErrors)
			}
		})
	}
}

func TestCollectSteps_FailingCollectorIsRecordedInItsOwnFolder(t *testing.T) {
	deps, _ := testDeps(t, map[v1alpha1.LogCategory][]collect.Collector{
		v1alpha1.LogCategoryBase: {
			&alpha{fakeCollector{func(acc *collect.Accessor) error { return acc.WriteOutput("a.txt", []byte("12345")) }}},
			&bravo{fakeCollector{func(*collect.Accessor) error { return errors.New("bravo boom") }}},
			&charlie{},
		},
	})

	var steps []Step
	var reports []string
	summary, err := runSteps(context.Background(), deps, func(_ context.Context, step Step) error {
		report, _ := os.ReadFile(filepath.Join(step.Dir, logCaptureReportLog))
		steps, reports = append(steps, step), append(reports, string(report))
		return nil
	})

	require.NoError(t, err)
	require.Len(t, steps, 3, "a failing collector must not stop the loop")
	assert.Equal(t, []bool{false, true, false}, []bool{steps[0].Failed, steps[1].Failed, steps[2].Failed})
	assert.Contains(t, reports[1], "bravo boom")
	assert.Contains(t, reports[1], "Base/bravo")
	assert.Empty(t, reports[0]+reports[2], "a failure must not leak into another step's folder")
	assert.Equal(t, 1, summary.Failed)
	assert.Equal(t, 2, summary.Files, "a.txt plus the error report")
}

func TestCollectSteps_CancelledContextStopsBeforeNextCollector(t *testing.T) {
	deps, _ := testDeps(t, fixture())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got []string
	summary, err := runSteps(ctx, deps, func(_ context.Context, step Step) error {
		got = append(got, step.Name)
		cancel()
		return nil
	})

	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"Base/alpha"}, got, "no collector may start after cancellation")
	assert.Equal(t, 1, summary.Steps, "the partial summary must be returned")
}

func TestCollectSteps_DiskChecks(t *testing.T) {
	tests := []struct {
		name        string
		free        func(call int) (uint64, error)
		wantRan     int
		wantErr     error
		wantSkipped []string
	}{
		{
			name:    "low at start runs nothing",
			free:    func(int) (uint64, error) { return testLimit - 1, nil },
			wantErr: ErrLowDisk,
		},
		{
			name: "low after two collectors skips the rest",
			free: func(call int) (uint64, error) {
				if call <= 2 {
					return 2 * testLimit, nil
				}
				return testLimit - 1, nil
			},
			wantRan:     2,
			wantSkipped: []string{"Base/delta", "Networking/charlie", "Networking/delta"},
		},
		{
			name:    "exactly at the limit keeps going",
			free:    func(int) (uint64, error) { return testLimit, nil },
			wantRan: 5,
		},
		{
			name:    "free disk error does not block the capture",
			free:    func(int) (uint64, error) { return 0, errors.New("statfs boom") },
			wantRan: 5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, tmp := testDeps(t, fixture())
			calls := 0
			deps.freeDisk = func(dir string) (uint64, uint64, error) {
				assert.Equal(t, os.TempDir(), dir, "the check must look at the temp disk")
				calls++
				free, err := tc.free(calls)
				return free, testTotal, err
			}

			handled := 0
			summary, err := runSteps(context.Background(), deps, func(context.Context, Step) error {
				handled++
				return nil
			})

			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err, "stopping early is not a failure")
			}
			assert.Equal(t, tc.wantRan, summary.Steps)
			assert.Equal(t, tc.wantRan, handled)
			var skipped []string
			for _, s := range summary.Skipped {
				assert.Equal(t, "low disk", s.Reason)
				skipped = append(skipped, s.Name)
			}
			assert.Equal(t, tc.wantSkipped, skipped)
			requireEmptyDir(t, tmp)
		})
	}
}

func TestFreeDiskBytes(t *testing.T) {
	free, total, err := freeDiskBytes(os.TempDir())
	require.NoError(t, err)
	assert.NotZero(t, total)
	assert.LessOrEqual(t, free, total)

	_, _, err = freeDiskBytes(filepath.Join(t.TempDir(), "missing"))
	assert.Error(t, err)
}

func TestCategoriesFor(t *testing.T) {
	tests := []struct {
		name      string
		condition corev1.NodeConditionType
		want      []v1alpha1.LogCategory
	}{
		{
			name:      "accelerated hardware -> Device",
			condition: conditions.AcceleratedHardwareReady,
			want:      []v1alpha1.LogCategory{v1alpha1.LogCategoryBase, v1alpha1.LogCategoryDevice},
		},
		{
			name:      "networking -> Networking",
			condition: conditions.NetworkingReady,
			want:      []v1alpha1.LogCategory{v1alpha1.LogCategoryBase, v1alpha1.LogCategoryNetworking},
		},
		{
			name:      "kernel -> System",
			condition: conditions.KernelReady,
			want:      []v1alpha1.LogCategory{v1alpha1.LogCategoryBase, v1alpha1.LogCategorySystem},
		},
		{
			name:      "container runtime -> Runtime",
			condition: conditions.ContainerRuntimeReady,
			want:      []v1alpha1.LogCategory{v1alpha1.LogCategoryBase, v1alpha1.LogCategoryRuntime},
		},
		{
			name:      "unknown -> Base only",
			condition: corev1.NodeConditionType("SomethingUnknown"),
			want:      []v1alpha1.LogCategory{v1alpha1.LogCategoryBase},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CategoriesFor(tc.condition)
			assert.Equal(t, v1alpha1.LogCategory(v1alpha1.LogCategoryBase), got[0], "Base must always be first")
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a"), []byte("12345"), 0644)) // 5 bytes
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b"), []byte("678"), 0644))   // 3 bytes
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(sub, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "c"), []byte("90"), 0644)) // 2 bytes

	files, size, err := dirSize(dir)
	require.NoError(t, err)
	assert.Equal(t, 3, files)
	assert.Equal(t, int64(10), size)
}
