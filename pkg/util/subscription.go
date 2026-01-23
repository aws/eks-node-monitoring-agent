package util

import (
	"context"
	"sync"

	"golang.a2z.com/Eks-node-monitoring-agent/api/monitor"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	channelHandlerQueueSize = 1000
)

// creates a sequential workqueue, which prevents race conditions when trying to
// react to multiple channels. If there are sets of events that have no risk of
// causing race conditions, they should be handled separately.
func NewChannelHandlerGroup[T any]() *channelHandlerGroup[T] {
	return &channelHandlerGroup[T]{}
}

type channelHandlerGroup[T any] struct{}

// Start kicks off background goroutines to queue logs to the main sink, then
// blocks on a loop to continuously poll the sink. This ensures that no two jobs
// from the group can execute concurrently. All goroutines exit when ctx is cancelled.
func (sh *channelHandlerGroup[T]) Start(ctx context.Context, channelHandlers ...*channelHandler[T]) error {
	logger := log.FromContext(ctx)

	type logItem struct {
		Log     T
		Handler func(T) error
	}

	queue := make(chan logItem, channelHandlerQueueSize)

	var wg sync.WaitGroup
	for _, chHandler := range channelHandlers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case item, ok := <-chHandler.channel:
					if !ok {
						return
					}
					select {
					case <-ctx.Done():
						return
					case queue <- logItem{Log: item, Handler: chHandler.handler}:
					default:
						logger.Info("channel handler queue full, dropping item")
					}
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(queue)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case job, ok := <-queue:
			if !ok {
				return nil
			}
			if err := job.Handler(job.Log); err != nil {
				logger.Error(err, "error in channel handler")
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
		case msg, ok := <-ch.channel:
			if !ok {
				return nil
			}
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
