package tools

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type PEFileVersion struct {
	Major int
	Minor int
	Patch int
	Build int
}

func (v PEFileVersion) String() string {
	return fmt.Sprintf("%d.%d.%d.%d", v.Major, v.Minor, v.Patch, v.Build)
}

// ReadPEFileVersion reads VS_FIXEDFILEINFO without executing the Windows binary.
func ReadPEFileVersion(path string) (PEFileVersion, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PEFileVersion{}, err
	}
	signature := []byte{0xbd, 0x04, 0xef, 0xfe}
	for offset := 0; offset+24 <= len(data); offset++ {
		if data[offset] != signature[0] || data[offset+1] != signature[1] ||
			data[offset+2] != signature[2] || data[offset+3] != signature[3] {
			continue
		}
		if binary.LittleEndian.Uint32(data[offset+4:offset+8]) != 0x00010000 {
			continue
		}
		versionMS := binary.LittleEndian.Uint32(data[offset+8 : offset+12])
		versionLS := binary.LittleEndian.Uint32(data[offset+12 : offset+16])
		return PEFileVersion{
			Major: int(versionMS >> 16), Minor: int(versionMS & 0xffff),
			Patch: int(versionLS >> 16), Build: int(versionLS & 0xffff),
		}, nil
	}
	return PEFileVersion{}, errors.New("Windows file version resource not found")
}

// ValidateSKSERuntime rejects unsupported or mismatched Steam Skyrim/SKSE combinations.
func ValidateSKSERuntime(gameID, installPath string) error {
	requiredDLL, version, err := RequiredSKSERuntimeDLL(gameID, installPath)
	if err != nil {
		return err
	}
	if requiredDLL == "" {
		return nil
	}
	if info, err := os.Stat(filepath.Join(installPath, requiredDLL)); err != nil || info.IsDir() {
		return fmt.Errorf("SKSE runtime mismatch: %s requires %s", version.String(), requiredDLL)
	}
	return nil
}

// RequiredSKSERuntimeDLL returns the loader DLL required by a supported Skyrim runtime.
func RequiredSKSERuntimeDLL(gameID, installPath string) (string, PEFileVersion, error) {
	var gameExe string
	var supported map[[3]int]string
	switch gameID {
	case "skyrim":
		gameExe = "TESV.exe"
		supported = map[[3]int]string{{1, 9, 32}: "skse_1_9_32.dll"}
	case "skyrimse":
		gameExe = "SkyrimSE.exe"
		supported = map[[3]int]string{
			{1, 5, 97}:   "skse64_1_5_97.dll",
			{1, 6, 1170}: "skse64_1_6_1170.dll",
		}
	default:
		return "", PEFileVersion{}, nil
	}
	version, err := ReadPEFileVersion(filepath.Join(installPath, gameExe))
	if err != nil {
		return "", PEFileVersion{}, fmt.Errorf("reading %s runtime version: %w", gameExe, err)
	}
	requiredDLL, ok := supported[[3]int{version.Major, version.Minor, version.Patch}]
	if !ok {
		return "", version, fmt.Errorf("unsupported %s Steam runtime %s; supported runtimes are 1.9.32, 1.5.97, and 1.6.1170 as applicable",
			gameID, version.String())
	}
	return requiredDLL, version, nil
}
