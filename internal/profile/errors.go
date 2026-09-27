package profile

import "fmt"

type IdentityInvalidError struct {
	Name string
}

// Error returns the refused profile name.
func (e *IdentityInvalidError) Error() string {
	return fmt.Sprintf("invalid profile name or folder %q", e.Name)
}
