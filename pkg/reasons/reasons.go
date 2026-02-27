package reasons

var (

	// reasons for the AcceleratedHardwareReady condition.

	DCGMDiagnosticFailure = ReasonMeta{
		template:        "DCGMDiagnosticFailure",
		defaultSeverity: "Fatal",
	}
	DCGMError = ReasonMeta{
		template:        "DCGMError",
		defaultSeverity: "Fatal",
	}
	DCGMFieldError = ReasonMeta{
		template:        "DCGMFieldError%d",
		defaultSeverity: "Warning",
	}
	DCGMHealthCode = ReasonMeta{
		template:        "DCGMHealthCode%d",
		defaultSeverity: "Warning",
	}
	DCGMHealthCodeFatal = ReasonMeta{
		template:        "DCGMHealthCode%d",
		defaultSeverity: "Fatal",
	}
	NeuronDMAError = ReasonMeta{
		template:        "NeuronDMAError",
		defaultSeverity: "Fatal",
	}
	NeuronHBMUncorrectableError = ReasonMeta{
		template:        "NeuronHBMUncorrectableError",
		defaultSeverity: "Fatal",
	}
	NeuronNCUncorrectableError = ReasonMeta{
		template:        "NeuronNCUncorrectableError",
		defaultSeverity: "Fatal",
	}
	NeuronSRAMUncorrectableError = ReasonMeta{
		template:        "NeuronSRAMUncorrectableError",
		defaultSeverity: "Fatal",
	}
	NvidiaDeviceCountMismatch = ReasonMeta{
		template:        "NvidiaDeviceCountMismatch",
		defaultSeverity: "Warning",
	}
	NvidiaDoubleBitError = ReasonMeta{
		template:        "NvidiaDoubleBitError",
		defaultSeverity: "Fatal",
	}
	NvidiaNCCLError = ReasonMeta{
		template:        "NvidiaNCCLError",
		defaultSeverity: "Warning",
	}
	NvidiaNVLinkError = ReasonMeta{
		template:        "NvidiaNVLinkError",
		defaultSeverity: "Fatal",
	}
	NvidiaPCIeError = ReasonMeta{
		template:        "NvidiaPCIeError",
		defaultSeverity: "Warning",
	}
	NvidiaPageRetirement = ReasonMeta{
		template:        "NvidiaPageRetirement",
		defaultSeverity: "Warning",
	}
	NvidiaPowerError = ReasonMeta{
		template:        "NvidiaPowerError",
		defaultSeverity: "Warning",
	}
	NvidiaThermalError = ReasonMeta{
		template:        "NvidiaThermalError",
		defaultSeverity: "Warning",
	}
	NvidiaXIDError = ReasonMeta{
		template:        "NvidiaXID%dError",
		defaultSeverity: "Fatal",
	}
	NvidiaXIDWarning = ReasonMeta{
		template:        "NvidiaXID%dWarning",
		defaultSeverity: "Warning",
	}

	// reasons for the ContainerRuntimeReady condition.

	ContainerRuntimeFailed = ReasonMeta{
		template:        "ContainerRuntimeFailed",
		defaultSeverity: "Warning",
	}
	DeprecatedContainerdConfiguration = ReasonMeta{
		template:        "DeprecatedContainerdConfiguration",
		defaultSeverity: "Warning",
	}
	KubeletFailed = ReasonMeta{
		template:        "KubeletFailed",
		defaultSeverity: "Warning",
	}
	LivenessProbeFailures = ReasonMeta{
		template:        "LivenessProbeFailures",
		defaultSeverity: "Warning",
	}
	PodStuckTerminating = ReasonMeta{
		template:        "PodStuckTerminating",
		defaultSeverity: "Fatal",
	}
	ReadinessProbeFailures = ReasonMeta{
		template:        "ReadinessProbeFailures",
		defaultSeverity: "Warning",
	}
	RepeatedRestart = ReasonMeta{
		template:        "%sRepeatedRestart",
		defaultSeverity: "Warning",
	}
	ServiceFailedToStart = ReasonMeta{
		template:        "ServiceFailedToStart",
		defaultSeverity: "Warning",
	}

	// reasons for the KernelReady condition.

	AppBlocked = ReasonMeta{
		template:        "AppBlocked",
		defaultSeverity: "Warning",
	}
	AppCrash = ReasonMeta{
		template:        "AppCrash",
		defaultSeverity: "Warning",
	}
	ApproachingKernelPidMax = ReasonMeta{
		template:        "ApproachingKernelPidMax",
		defaultSeverity: "Warning",
	}
	ApproachingMaxOpenFiles = ReasonMeta{
		template:        "ApproachingMaxOpenFiles",
		defaultSeverity: "Warning",
	}
	ConntrackExceededKernel = ReasonMeta{
		template:        "ConntrackExceededKernel",
		defaultSeverity: "Warning",
	}
	ExcessiveZombieProcesses = ReasonMeta{
		template:        "ExcessiveZombieProcesses",
		defaultSeverity: "Warning",
	}
	ForkFailedOutOfPIDs = ReasonMeta{
		template:        "ForkFailedOutOfPIDs",
		defaultSeverity: "Fatal",
	}
	KernelBug = ReasonMeta{
		template:        "KernelBug",
		defaultSeverity: "Warning",
	}
	LargeEnvironment = ReasonMeta{
		template:        "LargeEnvironment",
		defaultSeverity: "Warning",
	}
	RapidCron = ReasonMeta{
		template:        "RapidCron",
		defaultSeverity: "Warning",
	}
	SoftLockup = ReasonMeta{
		template:        "SoftLockup",
		defaultSeverity: "Warning",
	}

	// reasons for the NetworkingReady condition.

	BandwidthInExceeded = ReasonMeta{
		template:        "BandwidthInExceeded",
		defaultSeverity: "Warning",
	}
	BandwidthOutExceeded = ReasonMeta{
		template:        "BandwidthOutExceeded",
		defaultSeverity: "Warning",
	}
	ConntrackExceeded = ReasonMeta{
		template:        "ConntrackExceeded",
		defaultSeverity: "Warning",
	}
	EFAErrorMetric = ReasonMeta{
		template:        "EFAErrorMetric",
		defaultSeverity: "Warning",
	}
	IPAMDInconsistentState = ReasonMeta{
		template:        "IPAMDInconsistentState",
		defaultSeverity: "Warning",
	}
	IPAMDNoIPs = ReasonMeta{
		template:        "IPAMDNoIPs",
		defaultSeverity: "Warning",
	}
	IPAMDNotReady = ReasonMeta{
		template:        "IPAMDNotReady",
		defaultSeverity: "Fatal",
	}
	IPAMDNotRunning = ReasonMeta{
		template:        "IPAMDNotRunning",
		defaultSeverity: "Fatal",
	}
	IPAMDRepeatedlyRestart = ReasonMeta{
		template:        "IPAMDRepeatedlyRestart",
		defaultSeverity: "Warning",
	}
	InterfaceNotRunning = ReasonMeta{
		template:        "InterfaceNotRunning",
		defaultSeverity: "Fatal",
	}
	InterfaceNotUp = ReasonMeta{
		template:        "InterfaceNotUp",
		defaultSeverity: "Fatal",
	}
	KubeProxyNotReady = ReasonMeta{
		template:        "KubeProxyNotReady",
		defaultSeverity: "Warning",
	}
	LinkLocalExceeded = ReasonMeta{
		template:        "LinkLocalExceeded",
		defaultSeverity: "Warning",
	}
	MACAddressPolicyMisconfigured = ReasonMeta{
		template:        "MACAddressPolicyMisconfigured",
		defaultSeverity: "Warning",
	}
	MissingDefaultRoutes = ReasonMeta{
		template:        "MissingDefaultRoutes",
		defaultSeverity: "Warning",
	}
	MissingIPRoutes = ReasonMeta{
		template:        "MissingIPRoutes",
		defaultSeverity: "Warning",
	}
	MissingIPRules = ReasonMeta{
		template:        "MissingIPRules",
		defaultSeverity: "Warning",
	}
	MissingLoopbackInterface = ReasonMeta{
		template:        "MissingLoopbackInterface",
		defaultSeverity: "Fatal",
	}
	NetworkSysctl = ReasonMeta{
		template:        "NetworkSysctl",
		defaultSeverity: "Warning",
	}
	PPSExceeded = ReasonMeta{
		template:        "PPSExceeded",
		defaultSeverity: "Warning",
	}
	PortConflict = ReasonMeta{
		template:        "PortConflict",
		defaultSeverity: "Warning",
	}
	UnexpectedRejectRule = ReasonMeta{
		template:        "UnexpectedRejectRule",
		defaultSeverity: "Warning",
	}

	// reasons for the StorageReady condition.

	EBSInstanceIOPSExceeded = ReasonMeta{
		template:        "EBSInstanceIOPSExceeded",
		defaultSeverity: "Warning",
	}
	EBSInstanceThroughputExceeded = ReasonMeta{
		template:        "EBSInstanceThroughputExceeded",
		defaultSeverity: "Warning",
	}
	EBSVolumeIOPSExceeded = ReasonMeta{
		template:        "EBSVolumeIOPSExceeded",
		defaultSeverity: "Warning",
	}
	EBSVolumeThroughputExceeded = ReasonMeta{
		template:        "EBSVolumeThroughputExceeded",
		defaultSeverity: "Warning",
	}
	EtcHostsMountFailed = ReasonMeta{
		template:        "EtcHostsMountFailed",
		defaultSeverity: "Warning",
	}
	IODelays = ReasonMeta{
		template:        "IODelays",
		defaultSeverity: "Warning",
	}
	KubeletDiskUsageSlow = ReasonMeta{
		template:        "KubeletDiskUsageSlow",
		defaultSeverity: "Warning",
	}
	XFSSmallAverageClusterSize = ReasonMeta{
		template:        "XFSSmallAverageClusterSize",
		defaultSeverity: "Warning",
	}
)
