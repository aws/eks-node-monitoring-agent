package util

import (
	"context"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// creates a sequential workqueue, which prevents race conditions when trying to
// react to multiple channels. If there are sets of events that have no risk of
// causing race conditions, they should be handled separately.
func NewChannelHandlerGroup[T any]() *channelHandlerGroup[T] {
	return &channelHandlerGroup[T]{}
}

type channelHandlerGroup[T any] struct{}

// Start kicks off a background goroutines to queue logs to the main sink, then
// blocks on a loop to continuously poll the sink. This ensures that no two jobs
// from the group can execute concurrently.
func (sh *channelHandlerGroup[T]) Start(ctx context.Context, channelHandlers ...*channelHandler[T]) error {
	log := log.FromContext(ctx)

	type logItem struct {
		Log     T
		Handler func(T) error
	}

	queue := make(chan logItem, 1000)

	for _, chHandler := range channelHandlers {
		// handlers should not block eachother
		go func() {
			for log := range chHandler.channel {
				queue <- logItem{Log: log, Handler: chHandler.handler}
			}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case job := <-queue:
			if err := job.Handler(job.Log); err != nil {
				log.Error(err, "error in channel handler")
			}
		}
	}
}

func NewChannelHandler[T any](handler func(T) error, channel <-chan T) *channelHandler[T] {
	return &channelHandler[T]{
		channel: channel,
		handler: handler,
	}
}

type channelHandler[T any] struct {
	channel <-chan T
	handler func(T) error
}

func (ch *channelHandler[T]) Start(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg := <-ch.channel:
			if err := ch.handler(msg); err != nil {
				log.FromContext(ctx).Error(err, "error in channel handler")
			}
		}
	}
}

type SubscriptionArgs[T any] struct {
	SubscriptionFn func() (<-chan T, error)
	Handler        func(T) error
}

func NewChannelHandlerFromSubscriptionArgs[T any](mgr monitor.Manager, subArgs SubscriptionArgs[T]) (*channelHandler[T], error) {
	channel, err := subArgs.SubscriptionFn()
	if err != nil {
		return nil, err
	}
	return NewChannelHandler(subArgs.Handler, channel), nil
}
