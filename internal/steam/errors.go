package steam

import "errors"

var ErrRootNotFound = errors.New("steam root not found")
var ErrAppManifestNotFound = errors.New("steam app manifest not found")
