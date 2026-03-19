# bottlerocket/

Bottlerocket OS-specific diagnostics: application inventory and logdog support bundle.

**Collector source:** [`pkg/log_collector/collect/system.go` – `bottlerocket()`](../../../pkg/log_collector/collect/system.go)

**Collected only on:** Bottlerocket nodes (tag `bottlerocket`).

---

## Files

### `application-inventory.json`

Installed software packages and their versions on the Bottlerocket node.

- **Source:** File copy of `/usr/share/bottlerocket/application-inventory.json`
- **Linux syscall:** `open(2)`, `read(2)`
- **Content:** JSON array of installed packages with name, version, and source information. This is the Bottlerocket equivalent of `rpm -qa`.

**Sample output (truncated):**
```json
[
  {
    "name": "containerd",
    "version": "1.7.25",
    "source": "bottlerocket-core-kit"
  },
  {
    "name": "kubelet",
    "version": "1.32.x",
    "source": "bottlerocket-core-kit"
  },
  {
    "name": "eks-node-monitoring-agent",
    "version": "1.x.x",
    "source": "bottlerocket-core-kit"
  }
]
```

---

## `logdog/`

Output from the Bottlerocket `logdog` diagnostic tool.

### `logdog/command-output.log`

Standard output and exit status of the `logdog` command invocation.

- **Command:** `logdog`
- **Linux syscall:** `execve(2)` + `pipe(2)` + `read(2)` (subprocess output capture)
- **Content:** `logdog` progress messages and any errors encountered during log collection. `logdog` itself does not print the collected logs — it writes them to a tarball.

**Sample output:**
```
Collecting logs...
Writing archive to /var/log/support/bottlerocket-logs.tar.gz
Done.
```

### `logdog/bottlerocket-logs.tar.gz`

The Bottlerocket support bundle tarball produced by `logdog`.

- **Source:** File copy of `/var/log/support/bottlerocket-logs.tar.gz`
- **Linux syscall:** `open(2)`, `read(2)`
- **Content:** A compressed tar archive containing Bottlerocket-specific logs and diagnostics collected by `logdog`, including:
  - `journald` logs for all Bottlerocket services
  - Bottlerocket settings (from the API server)
  - Network configuration
  - Container runtime state
  - Boot logs

To inspect the contents:
```bash
tar -tzf bottlerocket-logs.tar.gz
tar -xzf bottlerocket-logs.tar.gz -C /tmp/bottlerocket-logs/
```
