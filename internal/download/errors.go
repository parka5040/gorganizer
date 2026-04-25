package download

import "errors"

var (
	ErrInvalidNXMURI      = errors.New("download: invalid NXM URI")
	ErrUnknownSlug        = errors.New("download: unknown game slug")
	ErrDownloadFailed     = errors.New("download: HTTP download failed")
	ErrUnsupportedArchive = errors.New("download: unsupported archive format")
)
