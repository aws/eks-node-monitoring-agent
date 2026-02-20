package observer

import (
	"context"
	"fmt"

	"github.com/aws/eks-node-monitoring-agent/api/monitor/resource"
)

// ObserverConstructorMap maps resource types to their constructor functions
var ObserverConstructorMap = map[resource.Type]ObserverConstructorFunc{}

// ObserverConstructorFunc is a function that creates an observer for a resource type
type ObserverConstructorFunc func([]resource.Part) (Observer, error)

// RegisterObserverConstructor registers an observer constructor for a resource type
func RegisterObserverConstructor(resourceType resource.Type, constructor ObserverConstructorFunc) {
	if _, exists := ObserverConstructorMap[resourceType]; exists {
		panic(fmt.Sprintf("observer constructor already registered for resource type: %s", resourceType))
	}
	ObserverConstructorMap[resourceType] = constructor
}

// Observer watches a resource and broadcasts events to subscribers
type Observer interface {
	// Subscribe returns a channel that receives events from this observer
	Subscribe() <-chan string

	// Identifier returns a unique identifier for this observer
	Identifier() string

	// Init initializes and starts the observer
	Init(ctx context.Context) error
}

// BaseObserver provides common functionality for observers
type BaseObserver struct {
	subscribers []chan string
}

// Subscribe creates a new subscription channel
func (o *BaseObserver) Subscribe() <-chan string {
	subscriber := make(chan string, 1000)
	o.subscribers = append(o.subscribers, subscriber)
	return subscriber
}

// Broadcast sends a message to all subscribers (non-blocking)
func (o *BaseObserver) Broadcast(source, message string) {
	for _, subscriber := range o.subscribers {
		select {
		case subscriber <- message:
		default:
			// Drop message if subscriber's buffer is full
		}
	}
}
