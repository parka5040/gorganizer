package download

import (
	"fmt"
	"strings"
)

type MainFileOptions struct {
	RejectMentions      []string
	PreferMentions      []string
	RequireVersionAnyOf []string
	FailOnAmbiguous     bool
}

// SelectMainFile chooses a MAIN-category Nexus file using the supplied criteria.
func SelectMainFile(files []NexusFileDetails, opts MainFileOptions) (*NexusFileDetails, error) {
	if opts.FailOnAmbiguous && len(opts.PreferMentions) > 0 {
		return nil, fmt.Errorf("%w: PreferMentions cannot be combined with FailOnAmbiguous", ErrInvalidMainFileOptions)
	}
	candidates := make([]*NexusFileDetails, 0, len(files))
	for i := range files {
		file := &files[i]
		if !strings.EqualFold(file.CategoryName, "MAIN") || mentionsAny(file, opts.RejectMentions) {
			continue
		}
		if len(opts.RequireVersionAnyOf) > 0 && !mentionsAnyVersion(file, opts.RequireVersionAnyOf) {
			continue
		}
		candidates = append(candidates, file)
	}
	if len(candidates) == 0 {
		return nil, ErrNoMainFile
	}
	if opts.FailOnAmbiguous {
		var primary *NexusFileDetails
		primaryCount := 0
		for _, file := range candidates {
			if file.IsPrimary {
				primary = file
				primaryCount++
			}
		}
		if primaryCount == 1 {
			return primary, nil
		}
		if len(candidates) == 1 {
			return candidates[0], nil
		}
		return nil, fmt.Errorf("%w: %d candidates", ErrAmbiguousMainFile, len(candidates))
	}

	chosen := candidates[0]
	chosenPreferred := mentionsAny(chosen, opts.PreferMentions)
	for _, file := range candidates[1:] {
		preferred := mentionsAny(file, opts.PreferMentions)
		if preferred && !chosenPreferred || preferred == chosenPreferred && file.FileID > chosen.FileID {
			chosen = file
			chosenPreferred = preferred
		}
	}
	return chosen, nil
}

// mentionsAny reports whether file mentions any non-empty needle.
func mentionsAny(file *NexusFileDetails, needles []string) bool {
	for _, needle := range needles {
		if mentions(file, needle) {
			return true
		}
	}
	return false
}

// mentionsAnyVersion reports whether file mentions any non-empty version in dotted, underscored, or hyphenated form.
func mentionsAnyVersion(file *NexusFileDetails, versions []string) bool {
	for _, version := range versions {
		for _, variant := range []string{version, strings.ReplaceAll(version, ".", "_"), strings.ReplaceAll(version, ".", "-")} {
			if mentions(file, variant) {
				return true
			}
		}
	}
	return false
}

// mentions reports whether the file's name, file name, or description contains needle, ignoring case and never matching a blank needle.
func mentions(file *NexusFileDetails, needle string) bool {
	if strings.TrimSpace(needle) == "" {
		return false
	}
	needle = strings.ToLower(needle)
	return strings.Contains(strings.ToLower(file.Name), needle) ||
		strings.Contains(strings.ToLower(file.FileName), needle) ||
		strings.Contains(strings.ToLower(file.Description), needle)
}
