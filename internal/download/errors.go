package download

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidNXMURI      = errors.New("download: invalid NXM URI")
	ErrUnknownSlug        = errors.New("download: unknown game slug")
	ErrDownloadFailed     = errors.New("download: HTTP download failed")
	ErrUnsupportedArchive = errors.New("download: unsupported archive format")
)

type NXMExpiredError struct {
	URI string
}

func (e *NXMExpiredError) Error() string {
	return fmt.Sprintf("NXM download link expired: %s", e.URI)
}

type DownloadNotFoundError struct {
	ID string
}

func (e *DownloadNotFoundError) Error() string {
	return fmt.Sprintf("download %q not found", e.ID)
}
