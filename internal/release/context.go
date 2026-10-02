package release

import (
	"context"
	"io"
)

type contextReader struct {
	ctx context.Context
	io.Reader
}

// Read stops a copy when its context ends.
func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
