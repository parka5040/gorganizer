package dto

import "testing"

// TestInstallBusyOperationNames keeps the GUI-visible install fence operations stable.
func TestInstallBusyOperationNames(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{BusyOperationInstall, "install"},
		{BusyOperationRegisterInstall, "register_install"},
		{BusyOperationExtractOverwrite, "extract_overwrite"},
		{BusyOperationRecovery, "recovery"},
	} {
		if tc.got != tc.want {
			t.Errorf("busy operation = %q, want %q", tc.got, tc.want)
		}
	}
}
