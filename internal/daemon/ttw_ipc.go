package daemon

import (
	"time"

	"github.com/parka/gorganizer/internal/ipc"
)

// CheckTTWPrereqs (interface form: takes int, returns ipc.TTWPrereqResult).
// The internal-typed helper is CheckTTWPrereqsInternal.
func (d *Daemon) CheckTTWPrereqs(backend int) (ipc.TTWPrereqResult, error) {
	st, err := d.CheckTTWPrereqsInternal(TTWBackend(backend))
	if err != nil {
		return ipc.TTWPrereqResult{}, err
	}
	return ipc.TTWPrereqResult{
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

func (d *Daemon) PrepareTTWInstaller(userPath string, backend int) (ipc.TTWInstallerInfoResult, error) {
	info, err := d.PrepareTTWInstallerInternal(userPath, TTWBackend(backend))
	if err != nil {
		return ipc.TTWInstallerInfoResult{}, err
	}
	return ipc.TTWInstallerInfoResult{
		Backend:       int(info.Backend),
		MpiFile:       info.MpiFile,
		InstallerExe:  info.InstallerExe,
		Version:       info.Version,
		AlternateMpis: info.AlternateMpis,
	}, nil
}

// EnsureNativeMpiInstaller (interface form: returns path, version, err).
func (d *Daemon) EnsureNativeMpiInstaller() (string, string, error) {
	path, err := d.ensureNativeMpiInstaller()
	if err != nil {
		return "", "", err
	}
	version, _ := readMpiInstallerVersion(path)
	return path, version, nil
}

func (d *Daemon) LaunchTTWInstaller(info ipc.TTWInstallerInfoResult, dataModName string) (string, error) {
	internal := TTWInstallerInfo{
		Backend:       TTWBackend(info.Backend),
		MpiFile:       info.MpiFile,
		InstallerExe:  info.InstallerExe,
		Version:       info.Version,
		AlternateMpis: info.AlternateMpis,
	}
	h, err := d.LaunchTTWInstallerInternal(internal, dataModName)
	if err != nil {
		return "", err
	}
	return h.ID, nil
}

// GetTTWInstallResult is the interface-form adapter around
func (d *Daemon) GetTTWInstallResult(id string, block bool) (ipc.TTWInstallResultData, error) {
	res, err := d.getTTWInstallResultInternal(id, block)
	if err != nil {
		return ipc.TTWInstallResultData{}, err
	}
	out := ipc.TTWInstallResultData{
		InstallerExitCode: res.InstallerExitCode,
		LayoutFixed:       res.LayoutFixed,
		DataModFileCount:  res.DataModFileCount,
		DataModBytes:      res.DataModBytes,
	}
	for _, d := range res.ChangedExesInRoot {
		out.ChangedExesInRoot = append(out.ChangedExesInRoot, ipc.TTWExeDeltaResult{
			RelPath: d.RelPath, Kind: d.Kind, Size: d.Size,
			MTime: d.MTime.UTC().Format(time.RFC3339), SHA256: d.SHA256,
		})
	}
	for _, d := range res.DataModExes {
		out.DataModExes = append(out.DataModExes, ipc.TTWExeDeltaResult{
			RelPath: d.RelPath, Kind: d.Kind, Size: d.Size,
			MTime: d.MTime.UTC().Format(time.RFC3339), SHA256: d.SHA256,
		})
	}
	return out, nil
}

// TranslateWinePath delegates to the tool manager. Synthetic games use
// their parent's prefix for the translation.
func (d *Daemon) TranslateWinePath(gameID, unixPath string) (string, error) {
	if d.toolMgr == nil {
		return "", nil
	}
	gc, ok := d.config.Games[gameID]
	if !ok {
		return "", nil
	}
	if gc.LinkedFromGameID != "" {
		eff, err := d.config.EffectiveGameConfig(gameID)
		if err == nil {
			gc = eff
		}
	}
	return d.toolMgr.WineTranslatePath(gameID, &gc, unixPath)
}

var _ ipc.TTWController = (*Daemon)(nil)
