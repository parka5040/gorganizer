package config

import "errors"

var (
	ErrInvalidGameID = errors.New("config: unknown game ID")
	ErrNoAPIKey      = errors.New("config: nexus API key not set")
)
