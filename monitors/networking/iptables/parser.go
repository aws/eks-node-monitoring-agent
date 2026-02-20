package iptables

import (
	"context"
	"fmt"
	"slices"
	"strings"

	log "sigs.k8s.io/controller-runtime/pkg/log"
)

type MatchSet struct {
	name string
	args string
}

type Match struct {
	name string
	set  []MatchSet
}

type IPTablesRule struct {
	table            string
	source           string
	comment          string
	matches          []Match
	Jump             string
	gotoChain        string
	rejectWith       string
	destination      string
	mark             string
	protocol         string
	tcpFlagsMask     string
	tcpFlagsSet      string
	connectionState  string
	mode             string
	probability      string
	DestinationPort  string
	destinationType  string
	sourceType       string
	raw              string
	toDestination    string
	destinationPorts string
	setXMark         string
	toSource         string
	nfMask           string
	ctMask           string
	ifc              string
	restoreMark      bool
	queueBypass      bool
	random           bool
	queueNum         string
	state            string
	outInterface     string
	limitIfcIn       bool
	fragment         bool
	sourcePort       string
	name             string
	rdest            bool
	rsource          bool
	set              bool
	reap             bool
	rttl             bool
	mask             string
	seconds          string
	rcheck           bool
	update           bool
	nfacctName       string
}

func (r IPTablesRule) IsReject() bool {
	return r.Jump == "REJECT" || r.Jump == "DROP" ||
		r.gotoChain == "REJECT" || r.gotoChain == "DROP" ||
		r.rejectWith != ""
}

func (rule IPTablesRule) IsExpectedRejectRule() bool {
	if rule.table == "KUBE-FORWARD" &&
		slices.ContainsFunc(rule.matches, func(m Match) bool { return m.name == "conntrack" }) &&
		rule.connectionState == "INVALID" {
		// -A KUBE-FORWARD -m conntrack --ctstate INVALID -j DROP
		return true
	} else if rule.table == "KUBE-FIREWALL" && strings.Contains(rule.comment, "block incoming localnet connections") {
		// -A KUBE-FIREWALL ! -s 127.0.0.0/8 -d 127.0.0.0/8 -m comment --comment "block incoming localnet connections" -m conntrack ! --ctstate RELATED,ESTABLISHED,DNAT -j DRO
		return true
	} else if rule.table == "KUBE-FIREWALL" && strings.Contains(rule.comment, "kubernetes firewall for dropping marked packets") {
		// -A KUBE-FIREWALL -m comment --comment "kubernetes firewall for dropping marked packets" -m mark --mark 0x8000/0x8000 -j DROP
		return true
	} else if (rule.table == "KUBE-SERVICES" || rule.table == "KUBE-EXTERNAL-SERVICES") &&
		(strings.Contains(rule.comment, "has no endpoints") ||
			strings.Contains(rule.comment, "has no local endpoints") ||
			strings.Contains(rule.rejectWith, "icmp-port-unreachable")) {
		// kube service with no endpoints
		return true
	}
	return false
}

func ParseIPTablesRule(line string) (*IPTablesRule, error) {
	logger := log.FromContext(context.TODO()).WithName("iptables-parser")

	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "-A") {
		return nil, fmt.Errorf("rules is missing table: %q", line)
	}
	segs := splitQuoted(line)

	getSeg := func(i int, invert bool) string {
		seg := ""
		if i >= 0 && i < len(segs) {
			seg = segs[i]
		}
		if invert {
			return "!" + seg
		}
		return seg
	}

	rule := IPTablesRule{raw: line}
	invert := false
	for i := 0; i < len(segs); i++ {
		opt := segs[i]
		switch opt {
		case "!":
			invert = true
		case "-i", "--in-interface":
			rule.ifc = getSeg(i+1, invert)
			i += 1
		case "-A":
			rule.table = getSeg(i+1, invert)
			i++
		case "-s", "--source":
			rule.source = getSeg(i+1, invert)
			i++
		case "-d", "--destination":
			rule.destination = getSeg(i+1, invert)
			i++
		case "-m", "--match":
			rule.matches = append(rule.matches, Match{
				name: getSeg(i+1, invert),
			})
			i++
		case "-j", "--jump":
			rule.Jump = getSeg(i+1, invert)
			i++
		case "-g", "--goto":
			rule.gotoChain = getSeg(i+1, invert)
			i++
		case "-p", "--protocol":
			rule.protocol = getSeg(i+1, invert)
			i++
		case "--name":
			rule.name = getSeg(i+1, invert)
			i++
		case "--comment":
			rule.comment = getSeg(i+1, invert)
			i++
		case "--mask":
			rule.mask = getSeg(i+1, invert)
			i++
		case "--mark":
			rule.mark = getSeg(i+1, invert)
			i++
		case "--reject-with":
			rule.rejectWith = getSeg(i+1, invert)
			i++
		case "--tcp-flags":
			rule.tcpFlagsMask = getSeg(i+1, invert)
			rule.tcpFlagsSet = getSeg(i+2, invert)
			i += 2
		case "--match-set":
			if len(rule.matches) > 0 {
				match := rule.matches[len(rule.matches)-1]
				match.set = append(match.set, MatchSet{
					name: getSeg(i+1, invert),
					args: getSeg(i+2, invert),
				})
			} else {
				logger.V(4).Info("invalid option format", "option", opt, "rule", segs)
			}
			i += 2
		case "--ctstate":
			rule.connectionState = getSeg(i+1, invert)
			i += 1
		case "--nfacct-name":
			rule.nfacctName = getSeg(i+1, invert)
			i += 1
		case "--nfmask":
			rule.nfMask = getSeg(i+1, invert)
			i += 1
		case "--ctmask":
			rule.ctMask = getSeg(i+1, invert)
			i += 1
		case "--mode":
			rule.mode = getSeg(i+1, invert)
			i += 1
		case "--probability":
			rule.probability = getSeg(i+1, invert)
			i += 1
		case "--sport", "--source-port":
			rule.sourcePort = getSeg(i+1, invert)
			i += 1
		case "--dport", "--destination-port":
			rule.DestinationPort = getSeg(i+1, invert)
			i += 1
		case "--dst-type":
			rule.destinationType = getSeg(i+1, invert)
			i += 1
		case "--src-type":
			rule.sourceType = getSeg(i+1, invert)
			i += 1
		case "--to-destination":
			rule.toDestination = getSeg(i+1, invert)
			i += 1
		case "--to-source":
			rule.toSource = getSeg(i+1, invert)
			i += 1
		case "--dports":
			rule.destinationPorts = getSeg(i+1, invert)
			i += 1
		case "--set-xmark":
			rule.setXMark = getSeg(i+1, invert)
			i += 1
		case "--restore-mark":
			rule.restoreMark = true
		case "--queue-num":
			rule.queueNum = getSeg(i+1, invert)
			i += 1
		case "--queue-bypass":
			rule.queueBypass = true
		case "--random-fully", "--random":
			rule.random = true
		case "--state":
			rule.state = getSeg(i+1, invert)
			i += 1
		case "-f", "--fragement":
			rule.fragment = true
		case "-o", "--out-interface":
			rule.outInterface = getSeg(i+1, invert)
			i += 1
		case "--seconds":
			rule.seconds = getSeg(i+1, invert)
			i += 1
		case "--limit-iface-in":
			rule.limitIfcIn = true
		case "--set":
			rule.set = true
		case "--rcheck":
			rule.rcheck = true
		case "--update":
			rule.update = true
		case "--rsource":
			rule.rsource = true
		case "--rdest":
			rule.rdest = true
		case "--reap":
			rule.reap = true
		case "--rttl":
			rule.rttl = true
		case "--validmark", "--invert":
			// ignore
		default:
			logger.V(6).Info("unsupported option", "option", opt, "rule", segs)
		}
		if invert && opt != "!" {
			invert = false
		}
	}
	return &rule, nil
}

func (r *IPTablesRule) String() string {
	return r.raw
}

func splitQuoted(line string) []string {
	quoted := false
	return strings.FieldsFunc(line, func(r rune) bool {
		if r == '"' {
			quoted = !quoted
		}
		return !quoted && r == ' '
	})
}
