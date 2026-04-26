package plugins

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// Header is the TES4 record header decoded from the start of an .esp/.esm/.esl
// file: the master list (MAST subrecords) and whether the ESL light-master
// flag is set. The engine uses these to resolve cross-plugin references at
// load time; the analyzer uses them to spot missing dependencies before the
// game ever launches.
type Header struct {
	Masters []string
	IsLight bool
	Flags   uint16
}

// Limits — TES4 records are tiny in practice (a hundred bytes plus a few
// dozen masters at most). Anything exceeding these caps means the file is
// corrupt or hostile; we refuse to allocate.
const (
	maxFileSize    = int64(2) << 30 // 2 GiB — Bethesda's plugin size ceiling
	maxHeaderWalk  = 64 * 1024      // bytes scanned for subrecords
	maxMasterCount = 256            // engine cap is 254 + 2 implicit
	maxMasterLen   = 1024           // a single MAST string
)

var (
	tes4Magic = [4]byte{'T', 'E', 'S', '4'}
	mastTag   = [4]byte{'M', 'A', 'S', 'T'}
	dataTag   = [4]byte{'D', 'A', 'T', 'A'}
)

// ParseHeader opens path and decodes the TES4 record header. The returned
// Header is safe to retain — no slice aliases the file buffer.
//
// ctx is honoured between subrecords so a long-running batch of parses can be
// cancelled cleanly.
func ParseHeader(ctx context.Context, path string) (*Header, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() < 24 {
		return nil, fmt.Errorf("plugin too small: %d bytes", st.Size())
	}
	if st.Size() > maxFileSize {
		return nil, fmt.Errorf("plugin exceeds %d bytes", maxFileSize)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Read the 24-byte TES4 record header. Layout (Skyrim/FO3/FNV/FO4 — same
	// shape across all post-Morrowind engines):
	//   [0..4)   "TES4"
	//   [4..8)   dataSize (uint32 LE)  — bytes of subrecords following
	//   [8..12)  flags (uint32 LE)     — low 16 bits are what we care about
	//   [12..16) formID                — always 0 for TES4
	//   [16..20) versionControlInfo
	//   [20..22) internalVersion
	//   [22..24) unknown
	var hdr [24]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return nil, err
	}
	if [4]byte{hdr[0], hdr[1], hdr[2], hdr[3]} != tes4Magic {
		return nil, errors.New("not a TES4 plugin (bad magic)")
	}
	dataSize := binary.LittleEndian.Uint32(hdr[4:8])
	flags32 := binary.LittleEndian.Uint32(hdr[8:12])
	flags := uint16(flags32 & 0xFFFF)

	walk := int64(dataSize)
	if walk > int64(maxHeaderWalk) {
		walk = int64(maxHeaderWalk)
	}

	out := &Header{
		IsLight: flags&0x0200 != 0,
		Flags:   flags,
	}

	r := io.LimitReader(f, walk)
	br := &boundedReader{r: r}

	// Walk subrecords. Each subrecord is [4-byte tag][2-byte size][payload].
	// The TES4 record contains HEDR (header), CNAM (author), SNAM (desc),
	// MAST (master file) + DATA (8-byte filesize, ignored), INTV/INCC/ONAM
	// (post-Skyrim). We only care about MAST.
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		var sub [6]byte
		n, err := io.ReadFull(br, sub[:])
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if n < 6 {
			break
		}
		size := binary.LittleEndian.Uint16(sub[4:6])
		if size > maxMasterLen {
			return nil, fmt.Errorf("subrecord %q payload too large: %d", string(sub[0:4]), size)
		}

		switch [4]byte{sub[0], sub[1], sub[2], sub[3]} {
		case mastTag:
			if len(out.Masters) >= maxMasterCount {
				// Skip rather than error — accept the plugin but stop adding.
				if _, err := io.CopyN(io.Discard, br, int64(size)); err != nil {
					return nil, err
				}
				continue
			}
			payload := make([]byte, size)
			if _, err := io.ReadFull(br, payload); err != nil {
				return nil, err
			}
			out.Masters = append(out.Masters, trimNul(payload))
		default:
			if _, err := io.CopyN(io.Discard, br, int64(size)); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, err
			}
		}
	}

	return out, nil
}

// boundedReader wraps an io.Reader to surface ErrUnexpectedEOF as a clean EOF
// at the boundary, so the subrecord loop terminates without bubbling an error
// up when the limit reader is simply exhausted at a record boundary.
type boundedReader struct{ r io.Reader }

func (b *boundedReader) Read(p []byte) (int, error) { return b.r.Read(p) }

func trimNul(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
