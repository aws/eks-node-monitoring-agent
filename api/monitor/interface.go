package monitor

import (
	"context"

	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
)

// Monitor defines the resources that a user wishes to subscribe to, and a top
// level handler that provides access to the delivered resource event as well as
// the state of the node object and a cached resource accessor.
type Monitor interface {
	// Name is a human readable identifier for the monitor
	Name() string
	// Returns all of the conditions owned by the monitor. Conditions are polled
	// at a fixed cadence.
	Conditions() []Condition
	// Register is an entrypoint for callers to setup their events and primary
	// logic. The Manager facilitates all operations for communicating events.
	Register(context.Context, Manager) error
}

type Manager interface {
	// Subscribe returns a channel to the stream of events coming from a
	// specified resource.
	Subscribe(resource.Type, []resource.Part) (<-chan string, error)
	// Notify is used to emit conditions directly to the manager. It will block
	// until either the message is sent or the context deadline is exceeded.
	Notify(context.Context, Condition) error
}

// Condition is a state provided by a monitor which holds information regarding
// component health on the node. This data may be delivered to different
// backends, so the fields should be generic for multiple use cases.
type Condition struct {
	// Reason is a short, PascalCase description of the error that led to this
	// issue type.
	Reason string
	// Message is a longer description of the issue with details.
	Message string
	// Used as a level as well as an indicator for which backend the issue
	// should be routed to.
	Severity
	// MinOccurrence is the minimal time the failure could occur before we export the condition.
	// default is 0 if not assigned
	MinOccurrences int64
}

// A gauge for how severe the issue is, and whether actions need to be taken
// for the node to recover from a bad state.
type Severity string

const (
	SeverityInfo    Severity = "Info"
	SeverityWarning Severity = "Warning"
	// fatal severity indicates the node has a permanent issue, only repairable
	// through an external action from a repair agent
	SeverityFatal Severity = "Fatal"
)
