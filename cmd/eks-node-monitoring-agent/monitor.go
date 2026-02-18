package main

import (
	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"golang.a2z.com/Eks-node-monitoring-agent/pkg/manager"
	corev1 "k8s.io/api/core/v1"
)

type monitorConfig struct {
	Monitor       monitor.Monitor
	ConditionType corev1.NodeConditionType
}

type conditionConfig = manager.NodeConditionConfig

type settings struct {
	MonitorConfigs   []monitorConfig
	ConditionConfigs map[corev1.NodeConditionType]conditionConfig
}

func NewMonitorSettings() *settings {
	return &settings{
		MonitorConfigs:   []monitorConfig{},
		ConditionConfigs: map[corev1.NodeConditionType]conditionConfig{},
	}
}

func (s *settings) Add(
	monitor monitor.Monitor,
	nodeConditionType corev1.NodeConditionType,
	readyReason, readyMessage string,
) {
	s.ConditionConfigs[nodeConditionType] = manager.NodeConditionConfig{
		ReadyReason:  readyReason,
		ReadyMessage: readyMessage,
	}
	s.MonitorConfigs = append(s.MonitorConfigs, monitorConfig{
		Monitor:       monitor,
		ConditionType: nodeConditionType,
	})
}
