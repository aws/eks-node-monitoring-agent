# E2E Test Framework

End-to-end tests for the EKS Node Monitoring Agent using [sigs.k8s.io/e2e-framework](https://github.com/kubernetes-sigs/e2e-framework).

## Structure

```
e2e/
├── main_test.go              # Test entry point
├── setup/
│   ├── e2e.go                # Environment configuration
│   ├── hooks.go              # Install/cleanup hooks
│   └── manifests/
│       └── agent.tpl.yaml    # Agent K8s manifests (templated)
├── suites/
│   ├── basic/                # Basic deployment validation
│   ├── monitors/             # Monitor detection tests
│   ├── logging/              # Console diagnostics tests
│   ├── addon/                # Addon configuration tests
│   └── nodediagnostic/       # NodeDiagnostic CRD tests
├── framework_extensions/
│   ├── client.go             # K8s manifest helpers
│   └── conditions.go         # Custom wait conditions
├── aws/                      # AWS helper utilities
├── k8s/                      # K8s helper utilities
├── metrics/                  # Metrics collection
└── monitoring/               # CloudWatch and profiling
```

## Running Tests

### Build the test binary

```bash
make build-e2e
```

### Run tests against an existing cluster

```bash
./bin/e2e.test --test.v --install=true --image=<NMA_IMAGE>
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--install` | `true` | Install agent manifests before tests |
| `--image` | (required) | NMA container image to deploy |
| `--stage` | `prod` | EKS stage for running tests |
| `--test.run` | | Filter tests by name pattern |
| `--test.timeout` | `10m` | Test timeout |

## Adding New Tests

### 1. Create a new suite (optional)

For a new category of tests, create a new package under `suites/`:

```
e2e/suites/
├── basic/           # Deployment validation
├── monitors/        # Monitor detection tests
└── myfeature/       # Your new test suite
```

### 2. Create test features

Each test is a "feature" using the e2e-framework pattern:

```go
package myfeature

import (
    "context"
    "testing"

    "sigs.k8s.io/e2e-framework/pkg/envconf"
    "sigs.k8s.io/e2e-framework/pkg/features"
)

func MyTest() features.Feature {
    return features.New("MyTest").
        WithLabel("suite", "myfeature").
        Assess("description of what is tested", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
            // Test logic here
            return ctx
        }).
        Feature()
}
```

### 3. Register the test

Add your test to `setup/e2e.go` in the `TestWrapper` function:

```go
import "github.com/aws/eks-node-monitoring-agent/e2e/suites/myfeature"

func TestWrapper(t *testing.T, Testenv env.Environment) {
    // Existing tests...
    Testenv.Test(t, myfeature.MyTest())
}
```

## CI Integration

Tests are triggered via PR comments using the `/ci` command.

### Examples

Run all tests on K8s 1.34:
```
/ci +workflow:k8s_versions 1.34
```

Run a specific test (use `Test/FeatureName` format):
```
/ci +workflow:k8s_versions 1.34 +workflow:test_filter Test/DaemonSetReady
```

Run multiple tests:
```
/ci +workflow:k8s_versions 1.34 +workflow:test_filter "Test/DaemonSetReady|Test/PodsHealthy"
```

Test on arm64:
```
/ci +workflow:k8s_versions 1.34 +workflow:arch arm64
```

### Available Basic Tests

| Filter | Description |
|--------|-------------|
| `Test/DaemonSetReady` | Verifies DaemonSet is ready |
| `Test/PodsHealthy` | Verifies all pods are running |
| `Test/CRDsInstalled` | Verifies NodeDiagnostic CRD exists |

See `.github/workflows/ci.yaml` for the full workflow configuration.
