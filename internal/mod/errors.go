package mod

import "errors"

var (
	ErrNoDataDir    = errors.New("mod: Data subdirectory not found")
	ErrModNotFound  = errors.New("mod: not found")
	ErrEmptyModList = errors.New("mod: modlist is empty")
)
