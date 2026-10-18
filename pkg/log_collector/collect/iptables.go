package collect

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strings"
)

type IPTables struct{}

var _ Collector = (*IPTables)(nil)

func (i IPTables) Collect(acc *Accessor) error {
	var merr error

	for _, tableType := range []string{"mangle", "filter", "nat"} {
		merr = errors.Join(merr, i.collectRules(acc, []string{"iptables", "--wait", "1", "--numeric", "--verbose", "--list", "--table", tableType}, fmt.Sprintf("networking/iptables-%s.txt", tableType)))
		merr = errors.Join(merr, i.collectRules(acc, []string{"ip6tables", "--wait", "1", "--numeric", "--verbose", "--list", "--table", tableType}, fmt.Sprintf("networking/ip6tables-%s.txt", tableType)))
	}

	merr = errors.Join(merr, i.collectRules(acc, []string{"iptables", "--wait", "1", "--numeric", "--verbose", "--list"}, "networking/iptables.txt"))
	merr = errors.Join(merr, i.collectRules(acc, []string{"ip6tables", "--wait", "1", "--numeric", "--verbose", "--list"}, "networking/ip6tables.txt"))

	merr = errors.Join(merr, acc.CommandOutput([]string{"iptables-save"}, "networking/iptables-save.txt", CommandOptionsNone))
	merr = errors.Join(merr, acc.CommandOutput([]string{"ip6tables-save"}, "networking/ip6tables-save.txt", CommandOptionsNone))

	merr = errors.Join(merr, ipvs(acc), ipset(acc))

	return merr
}

// ipvs collects the IPVS virtual server table, which holds the kube-proxy
// state when it runs in IPVS mode. ipvsadm is not installed on most nodes, so
// absence skips collection; individual command failures (e.g. the ip_vs
// module not being loaded) are recorded as best-effort.
func ipvs(acc *Accessor) error {
	if _, err := acc.CombinedOutput("ipvsadm", "--version"); err != nil {
		acc.logger.Info("ipvsadm not installed")
		return nil
	}
	return errors.Join(
		acc.appendOutput("networking/ipvsadm.txt", []byte("*** Output of ipvsadm --save ***\n")),
		acc.CommandOutput([]string{"ipvsadm", "--save"}, "networking/ipvsadm.txt", CommandOptionsAppend|CommandOptionsNoStderr|CommandOptionsIgnoreFailure),
		acc.appendOutput("networking/ipvsadm.txt", []byte("*** Output of ipvsadm --list --numeric --rate ***\n")),
		acc.CommandOutput([]string{"ipvsadm", "--list", "--numeric", "--rate"}, "networking/ipvsadm.txt", CommandOptionsAppend|CommandOptionsNoStderr|CommandOptionsIgnoreFailure),
		acc.appendOutput("networking/ipvsadm.txt", []byte("*** Output of ipvsadm --list --numeric --stats --exact ***\n")),
		acc.CommandOutput([]string{"ipvsadm", "--list", "--numeric", "--stats", "--exact"}, "networking/ipvsadm.txt", CommandOptionsAppend|CommandOptionsNoStderr|CommandOptionsIgnoreFailure),
	)
}

// ipset collects the ipset contents that iptables rules reference (used by
// kube-proxy in IPVS mode and some network policy implementations). ipset is
// not installed on most nodes, so absence skips collection.
func ipset(acc *Accessor) error {
	if _, err := acc.CombinedOutput("ipset", "version"); err != nil {
		acc.logger.Info("ipset not installed")
		return nil
	}
	return errors.Join(
		acc.appendOutput("networking/ipset.txt", []byte("*** Output of ipset --list ***\n")),
		acc.CommandOutput([]string{"ipset", "--list"}, "networking/ipset.txt", CommandOptionsAppend|CommandOptionsNoStderr|CommandOptionsIgnoreFailure),
		acc.appendOutput("networking/ipset.txt", []byte("*** Output of ipset --save ***\n")),
		acc.CommandOutput([]string{"ipset", "--save"}, "networking/ipset.txt", CommandOptionsAppend|CommandOptionsNoStderr|CommandOptionsIgnoreFailure),
	)
}

func (i IPTables) collectRules(acc *Accessor, cmd []string, filename string) error {
	output, err := acc.CombinedOutput(cmd...)
	if err != nil {
		return fmt.Errorf("error running %q, %w", cmd, err)
	}

	sc := bufio.NewScanner(bytes.NewReader(output))
	ruleCount := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if len(line) == 0 {
			continue
		}
		if strings.HasPrefix(line, "Chain") ||
			strings.HasPrefix(line, "num") ||
			strings.HasPrefix(line, "pkts") {
			continue
		}
		ruleCount += 1
	}
	output = append(output, []byte(fmt.Sprintf("=======\nTotal Number of Rules: %d\n", ruleCount))...)
	return acc.WriteOutput(filename, output)
}
