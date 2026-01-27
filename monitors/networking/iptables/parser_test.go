package iptables_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.a2z.com/Eks-node-monitoring-agent/monitors/networking/iptables"
)

func TestIPTablesRuleParser(t *testing.T) {
	t.Run("NotARule", func(t *testing.T) {
		_, err := iptables.ParseIPTablesRule("foo")
		assert.Error(t, err)
	})

	t.Run("UnsupportedFlag", func(t *testing.T) {
		_, err := iptables.ParseIPTablesRule("-A XX --not-a-flag")
		assert.NoError(t, err)
	})

	t.Run("ExpectedRejectRule", func(t *testing.T) {
		for _, ruleRaw := range []string{
			`-A KUBE-FORWARD -m conntrack --ctstate INVALID -m nfacct --nfacct-name ct_state_invalid_dropped_pkts -j DROP`,
			`-A KUBE-FORWARD -m conntrack --ctstate INVALID -j DROP`,
			`-A KUBE-FIREWALL ! -s 127.0.0.0/8 -d 127.0.0.0/8 -m comment --comment "block incoming localnet connections" -m conntrack ! --ctstate RELATED,ESTABLISHED,DNAT -j DROP`,
			`-A KUBE-FIREWALL -m comment --comment "kubernetes firewall for dropping marked packets" -m mark --mark 0x8000/0x8000 -j DROP`,
			`-A KUBE-SERVICES -d 10.100.31.155/32 -p tcp -m comment --comment "kube-system/aws-load-balancer-webhook-service:webhook-server has no endpoints" -m tcp --dport 443 -j REJECT --reject-with icmp-port-unreachable`,
		} {

			rule, err := iptables.ParseIPTablesRule(ruleRaw)
			assert.NoError(t, err)
			assert.Truef(t, rule.IsExpectedRejectRule(), ruleRaw)
		}
	})

	t.Run("NotExpectedRejectRule", func(t *testing.T) {
		for _, ruleRaw := range []string{
			`-A NOT-KUBE -m conntrack --ctstate INVALID`,
		} {
			rule, err := iptables.ParseIPTablesRule(ruleRaw)
			assert.NoError(t, err)
			assert.Falsef(t, rule.IsExpectedRejectRule(), ruleRaw)
		}
	})

	t.Run("RejectRule", func(t *testing.T) {
		for _, ruleRaw := range []string{
			`-A KUBE-FORWARD -m conntrack --ctstate INVALID -j DROP`,
			`-A KUBE-FIREWALL -m comment --comment "kubernetes firewall for dropping marked packets" -m mark --mark 0x8000/0x8000 -j DROP`,
			`-A KUBE-EXTERNAL-SERVICES -p tcp -m comment --comment "blah/blah has no local endpoints" -m addrtype --dst-type LOCAL -m tcp --dport 32012 -j DROP`,
			`-A SOMETHING-ELSE -m comment  -j DROP`,
			`-A TWISTLOCK-NET-OUTPUT -s 1.2.3.4/26 -p tcp -m tcp --tcp-flags FIN,SYN,RST,ACK SYN -m mark --mark 0x10101010 -m comment --comment TWISTLOCK-RULE -j REJECT --reject-with tcp-reset`,
		} {
			rule, err := iptables.ParseIPTablesRule(ruleRaw)
			assert.NoError(t, err)
			assert.Truef(t, rule.IsReject(), ruleRaw)
		}
	})

	t.Run("Coverage", func(t *testing.T) {
		for _, ruleRaw := range []string{
			`-A PREROUTING -i eni+ -m comment --comment "AWS, outbound connections" -j AWS-CONNMARK-CHAIN-0`,
			`-A AWS-SNAT-CHAIN-1 ! -o vlan+ -m comment --comment "AWS, SNAT" -m addrtype ! --dst-type LOCAL -j SNAT --to-source 192.168.58.147 --random-fully`,
			`-A AWS-CONNMARK-CHAIN-1 -m comment --comment "AWS, CONNMARK" -j CONNMARK --set-xmark 0x80/0x80`,
			`-A KUBE-SVC-JD5MR3NA4I4DYORP -m comment --comment "kube-system/kube-dns:metrics -> 192.168.40.102:9153" -m statistic --mode random --probability 0.50000000000 -j KUBE-SEP-WHCU5AJT4ABEO4SO`,
			`-A PREROUTING -i eni+ -m comment --comment "AWS, primary ENI" -j CONNMARK --restore-mark --nfmask 0x80 --ctmask 0x80`,
			`-A PREROUTING -i ens5 -m comment --comment "AWS, primary ENI" -m addrtype --dst-type LOCAL --limit-iface-in -j CONNMARK --set-xmark 0x80/0x80`,
			`-A ATTACK -m recent --update --name ATTACK --rsource --seconds 1200 --hitcount 3 -j DROP`,
			`-A INPUT -p tcp --dport 80 -m set --match-set blocklist src -j DROP`,
			`-A PREROUTING -p tcp --dport 8443 --jump DNAT --to-destination 129.94.5.88:5000`,
			`-A INPUT -i eth0 -m recent --update --name BLACKLIST --mask 255.255.255.255 --rsource -j DROP`,
			`-A INPUT -p tcp --sport 80 --dport 80 -m addrtype --src-type UNICAST ! -s 127.0.0.0/8 -j WEB`,
			`-A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT`,
			`-A INPUT -p tcp --tcp-flags SYN,ACK SYN,ACK --sport 443 -j NFQUEUE --queue-num 200 --queue-bypass`,
			// rest of flags i cant find good examples for
			`-A INPUT -g CUSTOM -f --rttl --reap --set --rcheck --rdest --dports 1024:3000 --invert --src-type LOCAL --match multiport -j DROP`,
		} {
			_, err := iptables.ParseIPTablesRule(ruleRaw)
			assert.NoError(t, err)
		}
	})

	t.Run("String", func(t *testing.T) {
		ruleRaw := `-A KUBE-FORWARD -m conntrack --ctstate INVALID -j DROP`
		rule, err := iptables.ParseIPTablesRule(ruleRaw)
		assert.NoError(t, err)
		assert.Equal(t, ruleRaw, rule.String())
	})
}
