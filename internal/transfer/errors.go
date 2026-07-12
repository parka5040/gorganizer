package transfer

import "fmt"

type TransferGameMismatchError struct {
	Want string
	Got  string
}

func (e *TransferGameMismatchError) Error() string {
	return fmt.Sprintf("transfer_game_mismatch:want=%s:got=%s", e.Want, e.Got)
}

type TransferSchemaError struct {
	Version int
}

func (e *TransferSchemaError) Error() string {
	return fmt.Sprintf("transfer_schema:version=%d", e.Version)
}

type TransferPathError struct {
	Entry string
}

func (e *TransferPathError) Error() string {
	return fmt.Sprintf("transfer_path:entry=%s", e.Entry)
}

type TransferCollisionError struct {
	Name string
}

func (e *TransferCollisionError) Error() string {
	return fmt.Sprintf("transfer_collision:name=%s", e.Name)
}
