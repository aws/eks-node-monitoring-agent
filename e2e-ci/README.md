# E2E Test Framework

End-to-end tests for the EKS Node Monitoring Agent using [sigs.k8s.io/e2e-framework](https://github.com/kubernetes-sigs/e2e-framework).

## Structure

```
e2e/
├── main_test.go              # Test entry point
├── go.mod                    # Go module
├── Makefile                  # Build targets
├── setup/
│   ├── e2e.go                # Environment configuration
│   ├── hooks.go              # Install/cleanup hooks
│   └── manifests/
│       └── agent.tpl.yaml    # Agent K8s manifests (templated)
├── suites/
│   └── basic/
│       └── deployment.go     # Basic deployment tests
└── framework_extensions/
    └── client.go             # K8s manifest helpers
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
| `--test.run` | | Filter tests by name pattern |
| `--test.timeout` | `10m` | Test timeout |

## Adding New Tests

### 1. Create a new suite (optional)

For a new category of tests, create a new package under `suites/`:

```
e2e/suites/
├── basic/           # Existing: deployment validation
├── networking/      # Example: network-related tests
```

### 2. Create test features

Each test is a "feature" using the e2e-framework pattern:

```go
// e2e/suites/myfeature/tests.go
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
            // Use cfg.Client() for K8s API access
            // Use t.Fatalf() for failures
            return ctx
        }).
        Feature()
}
```

### 3. Register the test

Add your test to `setup/e2e.go` in the `TestWrapper` function:

```go
import "github.com/aws/eks-node-monitoring-agent/e2e/suites/myfeature"

func TestWrapper(t *testing.T, testenv env.Environment) {
    testenv.Test(t,
        // Existing tests
        basic.DaemonSetReady(),
        // New tests
        myfeature.MyTest(),
    )
}
```

## CI Integration

Tests are triggered via PR comments using the `/ci` command. See `.github/workflows/ci.yaml` for the workflow configuration.
