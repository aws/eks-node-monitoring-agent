package nccl

import (
	"context"
	"regexp"

	"github.com/aws/eks-node-monitoring-agent/api/monitor"
	"github.com/aws/eks-node-monitoring-agent/pkg/reasons"
)

// [Thu Oct 10 03:06:53 2024] pt_main_thread[2536443]: segfault at 7f797fe00000 ip 00007f7c7ac69996 sp 00007f7c12fd7c30 error 4 in libnccl.so.2[7f7c7ac00000+d3d3000]
var regexNCCLSegfaultInLibnccl = regexp.MustCompile(`.*segfault at.*in libnccl\.so.*`)

func NewNCCLSystem(kmsg <-chan string) *ncclErrors {
	return &ncclErrors{kmsgChan: kmsg}
}

type ncclErrors struct {
	kmsgChan <-chan string
}

func (nccl *ncclErrors) Step(context.Context) ([]monitor.Condition, error) {
	var conditions []monitor.Condition
	if regexNCCLSegfaultInLibnccl.MatchString(<-nccl.kmsgChan) {
		conditions = append(conditions,
			reasons.NvidiaNCCLError.
				Builder().
				Message("NCCL communication error caused by segfault in libnccl.so").
				Build(),
		)
	}
	return conditions, nil
}
