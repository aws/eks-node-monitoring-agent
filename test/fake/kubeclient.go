package fake

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type FakeKubeClient struct {
	client.Client

	GetBehavior   func(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error
	PatchBehavior func(context.Context, client.Object, client.Patch, ...client.PatchOption) error
}

func (f *FakeKubeClient) Reset() {
	f.GetBehavior = nil
	f.PatchBehavior = nil
}

func (f *FakeKubeClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if f.GetBehavior != nil {
		return f.GetBehavior(ctx, key, obj, opts...)
	}
	return nil
}

func (f *FakeKubeClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if f.PatchBehavior != nil {
		return f.PatchBehavior(ctx, obj, patch, opts...)
	}
	return nil
}
