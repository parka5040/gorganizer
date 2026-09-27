package ipc

import (
	"fmt"
	"testing"

	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/transfer"
	"google.golang.org/grpc/codes"
)

// TestMapErrorRejectionTokensEscapeTheirValues locks the codes, messages and escaping of the archive, bundle and profile rejection tokens.
func TestMapErrorRejectionTokensEscapeTheirValues(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&download.ArchiveRejectedError{Reason: download.ArchiveRejectedLimit, Detail: "big"}, "archive_rejected:reason=limit"},
		{fmt.Errorf("extracting: %w", &download.ArchiveRejectedError{Reason: "a:b=c"}), "archive_rejected:reason=a%3Ab%3Dc"},
		{fmt.Errorf("zip entry: %w", download.ErrUnsafeArchive), "archive_rejected:reason=unsafe_entry"},
		{&transfer.BundleRejectedError{Reason: transfer.BundleRejectedProfileName, Item: "../.."}, "bundle_rejected:reason=profile_name:item=..%2F.."},
		{&transfer.BundleRejectedError{Reason: transfer.BundleRejectedLink, Item: "mods/M:a=b"}, "bundle_rejected:reason=link:item=mods%2FM%3Aa%3Db"},
		{&profile.IdentityInvalidError{Name: "a:b"}, "profile_identity_invalid:name=a%3Ab"},
	} {
		mapped, handled := MapError(tc.err)
		if !handled {
			t.Fatalf("MapError(%v) not handled", tc.err)
		}
		assertStatus(t, mapped, codes.InvalidArgument, tc.want)
	}
}
