package plugins

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

type Header struct {
	Masters []string
	IsLight bool
	Flags   uint16
}

const (
	maxFileSize    = int64(2) << 30
	maxHeaderWalk  = 64 * 1024
	maxMasterCount = 256
	maxMasterLen   = 1024
)

var (
	tes4Magic = [4]byte{'T', 'E', 'S', '4'}
	mastTag   = [4]byte{'M', 'A', 'S', 'T'}
	dataTag   = [4]byte{'D', 'A', 'T', 'A'}
)

// ParseHeader decodes the TES4 record header at path.
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
