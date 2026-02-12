package collect_test

import (
	"testing"

	"golang.a2z.com/Eks-node-monitoring-agent/pkg/log_collector/collect"
)

func TestLogCollectorAccessor(t *testing.T) {
	accessor, err := collect.NewAccessor(collect.Config{
		Root:        "/",
		Destination: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []collect.Collector{
		&collect.Instance{},
		&collect.Region{},
		&collect.CommonLogs{},
		&collect.Containerd{},
		&collect.CNI{},
		&collect.Kernel{},
		&collect.Disk{},
		&collect.SELinux{},
		&collect.Nvidia{},
		&collect.IPTables{},
		&collect.IPAMD{},
		&collect.System{},
		&collect.Nodeadm{},
		&collect.Throttles{},
		&collect.Sandbox{},
		&collect.Kubernetes{},
		&collect.Networking{},
	} {
		// these are not expected to succeed on the build host, but running
		// them for unit test coverage because they are non-destructive.
		c.Collect(accessor)
	}
}
