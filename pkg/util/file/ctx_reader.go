package file

import (
	"context"
	"io"
)

// NewContextReader wraps r so a copy of a large file stops at the next chunk
// boundary once ctx is cancelled, instead of running to the end. A read already
// blocked in the kernel is not interrupted.
func NewContextReader(ctx context.Context, r io.Reader) io.Reader {
	return &ctxReader{ctx: ctx, r: r}
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
