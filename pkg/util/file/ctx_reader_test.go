package file

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextReaderLiveContext(t *testing.T) {
	r := NewContextReader(context.Background(), bytes.NewReader([]byte("alpha")))
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "alpha", string(got))
}

func TestContextReaderCancelledBeforeFirstRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := NewContextReader(ctx, bytes.NewReader([]byte("alpha")))

	n, err := r.Read(make([]byte, 8))
	assert.Zero(t, n)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestContextReaderCancelledBetweenReads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := NewContextReader(ctx, bytes.NewReader([]byte("ab")))

	buf := make([]byte, 1)
	n, err := r.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.Equal(t, "a", string(buf))

	cancel()
	n, err = r.Read(buf)
	assert.Zero(t, n)
	assert.True(t, errors.Is(err, context.Canceled))
}
