package neuron

import (
	"context"
	"regexp"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor/resource"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/reasons"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/util"
)

var _ monitor.Monitor = (*neuronMonitor)(nil)

type neuronMonitor struct {
	manager monitor.Manager
}

type neuronMonitorRule struct {
	regex     *regexp.Regexp
	condition monitor.Condition
}

func (n *neuronMonitor) Name() string {
	return "neuron"
}

func (n *neuronMonitor) Conditions() []monitor.Condition {
	return []monitor.Condition{}
}

func (n *neuronMonitor) Register(ctx context.Context, mgr monitor.Manager) error {
	n.manager = mgr

	dmesg, err := mgr.Subscribe(resource.ResourceTypeDmesg, []resource.Part{})
	if err != nil {
		return err
	}

	for _, handler := range []interface{ Start(context.Context) error }{
		util.NewChannelHandler(n.handleNeuron, dmesg),
	} {
		go handler.Start(ctx)
	}

	return nil
}

func (n *neuronMonitor) handleNeuron(line string) error {
	for _, rule := range n.getNeuronMonitorRules() {
		if matches := rule.regex.FindStringSubmatch(line); matches != nil {
			if err := n.manager.Notify(context.Background(), rule.condition); err != nil {
				return err
			}
		}
	}

	return nil
}

// These patterns are based on AWS Neuron troubleshooting documentation:
// https://awsdocs-neuron.readthedocs-hosted.com/en/latest/neuron-runtime/nrt-troubleshoot.html
// and the open source Neuron Problem Detector configuration:
// https://github.com/aws-neuron/aws-neuron-sdk/blob/6b1d2c74c7d38db29a768010def16f548eef2f3d/src/k8/neuron-problem-detector/k8s-neuron-problem-detector-and-recovery-config.yml#L22
func (n *neuronMonitor) getNeuronMonitorRules() []neuronMonitorRule {
	return []neuronMonitorRule{
		{
			regex: regexp.MustCompile(".* NEURON_HW_ERR=SRAM_UNCORRECTABLE_ERROR .*"),
			condition: reasons.NeuronSRAMUncorrectableError.
				Builder().
				Message("Discovered SRAM Uncorrectable Error").
				Build(),
		},
		{
			regex: regexp.MustCompile(".* NEURON_HW_ERR=NC_UNCORRECTABLE_ERROR .*"),
			condition: reasons.NeuronNCUncorrectableError.
				Builder().
				Message("Discovered NC Uncorrectable Error").
				Build(),
		},
		{
			regex: regexp.MustCompile(".* NEURON_HW_ERR=HBM_UNCORRECTABLE_ERROR .*"),
			condition: reasons.NeuronHBMUncorrectableError.
				Builder().
				Message("Discovered HBM Uncorrectable Error").
				Build(),
		},
		{
			regex: regexp.MustCompile(".* NEURON_HW_ERR=DMA_ERROR .*"),
			condition: reasons.NeuronDMAError.
				Builder().
				Message("Discovered DMA Error").
				Build(),
		},
	}
}
