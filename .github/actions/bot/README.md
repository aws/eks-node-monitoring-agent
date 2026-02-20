# Bot

This GitHub Action parses commands from pull request comments and executes them.

Only authorized users (members and owners of this repository) are able to execute commands.

## Commands

Commands look like `/COMMAND ARGS`, for example:
```
/ci
```

### Available Commands

| Command | Description |
|---------|-------------|
| `/ci` | Trigger CI workflow with default parameters |
| `/ci cancel` | Cancel the most recent running CI for this PR |
| `/echo <text>` | Echo text back (for testing) |
| `/clear` | Remove all bot comments from the PR |

## Named Arguments

Some commands accept additional, named arguments specified on subsequent lines.
Named arguments look like `+NAME ARGS`, for example:

```
/ci
+workflow:k8s_versions 1.30,1.31
+workflow:arch arm64
+workflow:instance_type m6g.large
```

### CI Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `k8s_versions` | Latest from AWS API | Comma-separated K8s versions |
| `arch` | `amd64` | Architecture: `amd64` or `arm64` |
| `instance_type` | `t3.medium` | EC2 instance type |

## Examples

### Run CI with defaults
```
/ci
```

### Run CI on specific K8s version
```
/ci
+workflow:k8s_versions 1.30
```

### Run CI on ARM64 with GPU instance
```
/ci
+workflow:arch arm64
+workflow:instance_type g5g.xlarge
```

### Cancel running CI
```
/ci cancel
```

## Authorization

Only users with `OWNER` or `MEMBER` association can execute commands.

Note: Your org membership must be **public** for the `author_association` to be `MEMBER`.
Go to the org's member page, find yourself, and set the visibility to public.
