// IGNORE TEST COVERAGE (the file is not unit testable)

package exec

import (
	"errors"
	"strings"

	"github.com/aws/eks-node-monitoring-agent/pkg/config"
	"github.com/aws/eks-node-monitoring-agent/pkg/osext"
)

func GetRules() (rules []string, merr error) {
	rules = []string{}
	for _, cmd := range [][]string{
		{"ip", "rule", "show"},
		{"ip", "-6", "rule", "show"},
	} {
		out, err := osext.NewExec(config.HostRoot()).Command(cmd[0], cmd[1:]...).CombinedOutput()
		if err != nil {
			merr = errors.Join(merr, err)
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			rules = append(rules, line)
		}
	}

	return rules, nil
}

func GetRoutes() (routes []string, merr error) {
	routes = []string{}
	for _, cmd := range [][]string{
		{"ip", "route", "show", "table", "all"},
		{"ip", "-6", "route", "show", "table", "all"},
	} {
		out, err := osext.NewExec(config.HostRoot()).Command(cmd[0], cmd[1:]...).CombinedOutput()
		if err != nil {
			merr = errors.Join(merr, err)
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			routes = append(routes, line)
		}
	}
	return routes, nil
}
