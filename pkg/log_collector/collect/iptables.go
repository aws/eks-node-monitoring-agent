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

	return merr
}

func (i IPTables) collectRules(acc *Accessor, cmd []string, filename string) error {
	output, err := acc.Command(cmd[0], cmd[1:]...).CombinedOutput()
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
