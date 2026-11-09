package nitrosandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

const (
	// defaultDataBaseDir holds one directory per sandbox, named with the
	// 12-character short id, plus the jailer base and caches that are not
	// sandboxes. The runtime lets data_base_dir override it, so it must be
	// resolved from the shim config, not assumed: walking the wrong directory
	// would miss exactly the leaks a mismatched base directory produces.
	defaultDataBaseDir = "/var/lib/nitro-sandbox"
	// shimConfigPath is the runtime shim config; its data_base_dir key, when
	// set, moves the per-sandbox directories off defaultDataBaseDir.
	shimConfigPath = "/etc/containerd/nitro-sandbox.toml"
	dataBaseDirKey = "data_base_dir"
	// jailDirName is the jailer base under the data base dir; its children are
	// the per-sandbox jails, also named with the short id.
	jailDirName = "firecracker"
	// runStateDir holds one directory per sandbox, named with the full id, each
	// containing the vm.pid the runtime uses for orphan recovery. Not
	// configurable, unlike the data base dir.
	runStateDir = "/run/nitro-sandbox"
	vmPidFile   = "vm.pid"

	// leakGracePeriod keeps sandboxes that are still being created or torn down
	// out of the count. Teardown is best effort and racy by design, so a
	// directory younger than this says nothing about a leak.
	leakGracePeriod = 15 * time.Minute

	// leakExamplesInMessage bounds how many ids the message names.
	leakExamplesInMessage = 3
)

// Sandbox directories are named with containerd ids, so anything that is not
// hex of the right length is something else: dataBaseDir also holds
// erofs-cache, uid-locks and the jailer base.
var (
	shortIDPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)
	fullIDPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// diskSandbox is one sandbox as the host filesystem sees it.
type diskSandbox struct {
	// id is the full id when the run state directory was found, else the
	// 12-character short id taken from a data or jail directory.
	id string
	// shortID is the first 12 characters of id, which is how data and jail
	// directories are named.
	shortID string
	// modTime is the newest modification time seen across this sandbox's
	// directories, used only to skip sandboxes inside the grace period.
	modTime time.Time
	// vmAlive reports whether the recorded VMM process is still running as the
	// same process instance. False for a sandbox with no run state.
	vmAlive bool
}

// checkLeakedSandboxes reports LeakedSandboxResources when sandboxes that no
// longer belong to any pod hold every slot the node has, so no new isolated pod
// can start on it.
func (m *nitroSandboxMonitor) checkLeakedSandboxes(ctx context.Context) error {
	if !m.sandboxSupported() {
		return nil
	}

	// The container runtime's own view is the only way to tell a sandbox that
	// belongs to a pod from one that does not, so an unreadable list is not
	// evidence of a leak: it is the case where every sandbox would look leaked.
	running, err := m.criSandboxIDs(ctx)
	if err != nil {
		return fmt.Errorf("listing sandboxes from the container runtime, %w", err)
	}

	onDisk, err := m.diskSandboxes()
	if err != nil {
		return fmt.Errorf("reading sandbox state from disk, %w", err)
	}

	budget, err := m.slotBudget()
	if err != nil {
		return fmt.Errorf("reading the sandbox slot budget, %w", err)
	}
	if budget <= 0 {
		return nil
	}

	runningShort := make(map[string]struct{}, len(running))
	for _, id := range running {
		if len(id) >= 12 {
			runningShort[id[:12]] = struct{}{}
		}
	}

	var leaked []diskSandbox
	for _, sandbox := range onDisk {
		if _, ok := runningShort[sandbox.shortID]; ok {
			continue
		}
		if time.Since(sandbox.modTime) < leakGracePeriod {
			continue
		}
		leaked = append(leaked, sandbox)
	}

	if len(leaked) < budget {
		m.leakedPolls = 0
		return nil
	}

	m.leakedPolls++
	if m.leakedPolls < unhealthyPollsBeforeReporting {
		m.log.V(2).Info("sandbox slots look exhausted by leaks, waiting for confirmation",
			"leaked", len(leaked), "budget", budget, "polls", m.leakedPolls)
		return nil
	}

	return m.manager.Notify(ctx, reasons.LeakedSandboxResources.Builder().
		Message(leakMessage(leaked, budget)).
		Build())
}

// leakMessage names the counts and a few ids, and separates sandboxes whose VM
// is still running from ones that left only files behind, because the first
// still holds a slot and the second holds disk.
func leakMessage(leaked []diskSandbox, budget int) string {
	live := 0
	ids := make([]string, 0, leakExamplesInMessage)
	for _, sandbox := range leaked {
		if sandbox.vmAlive {
			live++
		}
		if len(ids) < leakExamplesInMessage {
			ids = append(ids, sandbox.shortID)
		}
	}
	slices.Sort(ids)

	detail := fmt.Sprintf("%d with a running microVM", live)
	if live == 0 {
		detail = "none still running"
	}
	return fmt.Sprintf(
		"%d of %d nitro-sandbox slots are held by sandboxes with no pod (%s), so this node cannot start new isolated pods (e.g. %s)",
		len(leaked), budget, detail, strings.Join(ids, ", "))
}

// hostDiskSandboxes collects sandboxes from the three places the runtime keeps
// per-sandbox state: the data directory, the jailer directory and the runtime
// state directory. Data and jail directories are named with the short id and
// the state directory with the full id, so they are correlated by prefix.
func hostDiskSandboxes() ([]diskSandbox, error) {
	baseDir := resolveDataBaseDir()
	byShortID := map[string]*diskSandbox{}

	track := func(id string, modTime time.Time) *diskSandbox {
		shortID := id
		if len(id) > 12 {
			shortID = id[:12]
		}
		sandbox, ok := byShortID[shortID]
		if !ok {
			sandbox = &diskSandbox{id: id, shortID: shortID}
			byShortID[shortID] = sandbox
		}
		// Prefer the full id when both spellings are seen.
		if len(id) > len(sandbox.id) {
			sandbox.id = id
		}
		if modTime.After(sandbox.modTime) {
			sandbox.modTime = modTime
		}
		return sandbox
	}

	// Data directories, skipping the jailer base and the caches that share the
	// directory but are not sandboxes.
	for _, entry := range readDirEntries(config.ToHostPath(baseDir)) {
		if !entry.IsDir() || !shortIDPattern.MatchString(entry.Name()) {
			continue
		}
		track(entry.Name(), entryModTime(entry))
	}

	// Jail directories.
	for _, entry := range readDirEntries(config.ToHostPath(filepath.Join(baseDir, jailDirName))) {
		if !entry.IsDir() || !shortIDPattern.MatchString(entry.Name()) {
			continue
		}
		track(entry.Name(), entryModTime(entry))
	}

	// Runtime state directories, which carry the VMM identity.
	for _, entry := range readDirEntries(config.ToHostPath(runStateDir)) {
		if !entry.IsDir() || !fullIDPattern.MatchString(entry.Name()) {
			continue
		}
		sandbox := track(entry.Name(), entryModTime(entry))
		sandbox.vmAlive = vmProcessAlive(filepath.Join(config.ToHostPath(runStateDir), entry.Name(), vmPidFile))
	}

	sandboxes := make([]diskSandbox, 0, len(byShortID))
	for _, sandbox := range byShortID {
		sandboxes = append(sandboxes, *sandbox)
	}
	return sandboxes, nil
}

// resolveDataBaseDir returns the sandbox base directory, honouring a
// data_base_dir override in the shim config so the walk follows the same
// directory the runtime writes to. Falls back to the default when the config
// is absent or does not set the key.
func resolveDataBaseDir() string {
	content, err := os.ReadFile(config.ToHostPath(shimConfigPath))
	if err != nil {
		return defaultDataBaseDir
	}
	for line := range strings.Lines(string(content)) {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found || strings.TrimSpace(key) != dataBaseDirKey {
			continue
		}
		if dir := strings.Trim(strings.TrimSpace(value), `"'`); dir != "" {
			return dir
		}
	}
	return defaultDataBaseDir
}

// readDirEntries returns a directory's entries, treating an unreadable
// directory as empty: on a node that has never run a sandbox these paths do not
// exist, which is not an error.
func readDirEntries(dir string) []os.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return entries
}

func entryModTime(entry os.DirEntry) time.Time {
	info, err := entry.Info()
	if err != nil {
		// Unknown age is treated as new, so the entry is skipped rather than
		// counted as a leak.
		return time.Now()
	}
	return info.ModTime()
}

// persistedVMPid is the runtime's vm.pid file: the VMM pid plus the pidfs inode
// id of that process instance.
type persistedVMPid struct {
	Pid     uint32 `json:"pid"`
	InodeID uint64 `json:"inode_id"`
}

// vmProcessAlive reports whether the VMM recorded in vm.pid is still running as
// the same process instance. The inode comparison is what makes this safe: pids
// are recycled, and the runtime records the pidfs inode for exactly this reason.
func vmProcessAlive(vmPidPath string) bool {
	content, err := os.ReadFile(vmPidPath)
	if err != nil {
		return false
	}
	var record persistedVMPid
	if err := json.Unmarshal(content, &record); err != nil {
		return false
	}

	fd, err := unix.PidfdOpen(int(record.Pid), 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return false
	}
	return stat.Ino == record.InodeID
}

// hostSlotBudget reads the number of sandbox slots the enclave driver has been
// given, which is what the runtime's device plugin advertises to kubelet.
func hostSlotBudget() (int, error) {
	content, err := os.ReadFile(config.ToHostPath(pcieSlots))
	if err != nil {
		return 0, err
	}
	var slots int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(content)), "%d", &slots); err != nil {
		return 0, fmt.Errorf("parsing %s contents %q, %w", pcieSlots, strings.TrimSpace(string(content)), err)
	}
	return slots, nil
}
