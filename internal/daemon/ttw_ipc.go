package daemon

import (
	"time"

	"github.com/parka/gorganizer/internal/dto"
)

// CheckTTWPrereqs (interface form: takes int, returns dto.TTWPrereqResult).
func (tt *TTWService) CheckTTWPrereqs(backend int) (dto.TTWPrereqResult, error) {
	st, err := tt.CheckTTWPrereqsInternal(TTWBackend(backend))
	if err != nil {
		return dto.TTWPrereqResult{}, err
	}
	return dto.TTWPrereqResult{
		Backend:               int(st.Backend),
		GstreamerInstalled:    st.GstreamerInstalled,
		GstreamerCodecsHint:   st.GstreamerCodecsHint,
		XdeltaInstalled:       st.XdeltaInstalled,
		DiskSpaceAvailable:    st.DiskSpaceAvailable,
		DiskSpaceRequired:     st.DiskSpaceRequired,
		FNVVanilla:            st.FNVVanilla,
		MpiInstallerPath:      st.MpiInstallerPath,
		MpiInstallerVersion:   st.MpiInstallerVersion,
		PrefixExists:          st.PrefixExists,
		HasDotnet48:           st.HasDotnet48,
		DotNet48ReleaseRev:    st.DotNet48ReleaseRev,
		HasMsxml6:             st.HasMsxml6,
		HasVcrun2022:          st.HasVcrun2022,
		HasCorefonts:          st.HasCorefonts,
		MonoNeedsRemoval:      st.MonoNeedsRemoval,
		SteamRunning:          st.SteamRunning,
		ProtontricksAvailable: st.ProtontricksAvailable,
		WinetricksAvailable:   st.WinetricksAvailable,
		Missing:               st.Missing,
	}, nil
}

func (tt *TTWService) PrepareTTWInstaller(userPath string, backend int) (dto.TTWInstallerInfoResult, error) {
	info, err := tt.PrepareTTWInstallerInternal(userPath, TTWBackend(backend))
	if err != nil {
		return dto.TTWInstallerInfoResult{}, err
	}
	return dto.TTWInstallerInfoResult{
		Backend:       int(info.Backend),
		MpiFile:       info.MpiFile,
		InstallerExe:  info.InstallerExe,
		Version:       info.Version,
		AlternateMpis: info.AlternateMpis,
	}, nil
}

// EnsureNativeMpiInstaller (interface form: returns path, version, err).
func (tt *TTWService) EnsureNativeMpiInstaller() (string, string, error) {
	path, err := tt.ensureNativeMpiInstaller()
	if err != nil {
		return "", "", err
	}
	version, _ := readMpiInstallerVersion(path)
	return path, version, nil
}

func (tt *TTWService) LaunchTTWInstaller(info dto.TTWInstallerInfoResult, dataModName string) (string, error) {
	internal := TTWInstallerInfo{
		Backend:       TTWBackend(info.Backend),
		MpiFile:       info.MpiFile,
		InstallerExe:  info.InstallerExe,
		Version:       info.Version,
		AlternateMpis: info.AlternateMpis,
	}
	h, err := tt.LaunchTTWInstallerInternal(internal, dataModName)
	if err != nil {
		return "", err
	}
	return h.ID, nil
}

// GetTTWInstallResult is the interface-form adapter around
func (tt *TTWService) GetTTWInstallResult(id string, block bool) (dto.TTWInstallResultData, error) {
	res, err := tt.getTTWInstallResultInternal(id, block)
	if err != nil {
		return dto.TTWInstallResultData{}, err
	}
	out := dto.TTWInstallResultData{
		InstallerExitCode: res.InstallerExitCode,
		LayoutFixed:       res.LayoutFixed,
		DataModFileCount:  res.DataModFileCount,
		DataModBytes:      res.DataModBytes,
	}
	for _, d := range res.ChangedExesInRoot {
		out.ChangedExesInRoot = append(out.ChangedExesInRoot, dto.TTWExeDeltaResult{
			RelPath: d.RelPath, Kind: d.Kind, Size: d.Size,
			MTime: d.MTime.UTC().Format(time.RFC3339), SHA256: d.SHA256,
		})
	}
	for _, d := range res.DataModExes {
		out.DataModExes = append(out.DataModExes, dto.TTWExeDeltaResult{
			RelPath: d.RelPath, Kind: d.Kind, Size: d.Size,
			MTime: d.MTime.UTC().Format(time.RFC3339), SHA256: d.SHA256,
		})
	}
	return out, nil
}

// TranslateWinePath delegates to the tool manager. Synthetic games use
func (tt *TTWService) TranslateWinePath(gameID, unixPath string) (string, error) {
	if tt.s.toolMgr == nil {
		return "", nil
	}
	gc, ok := tt.s.config.Games[gameID]
	if !ok {
		return "", nil
	}
	if gc.LinkedFromGameID != "" {
		eff, err := tt.s.config.EffectiveGameConfig(gameID)
		if err == nil {
			gc = eff
		}
	}
	return tt.s.toolMgr.WineTranslatePath(gameID, &gc, unixPath)
}
