#pragma once

#include <QString>

namespace gorganizer {

enum class InstallKind { Development, Prebuilt, Source, Other };

struct AppInstallLayout {
    InstallKind kind = InstallKind::Other;
    QString runningVersion;
    QString runningBase;
    QString dataHome;
    QString releasesRoot;
    QString bundleDir;
    QString scriptPath;
};

// Identifies the running GUI's release layout without following bundle-file symlinks.
AppInstallLayout detectInstallLayout();
// Reads the selected release name from the releases directory's current link.
bool readInstalledVersion(const QString& releasesRoot, QString* version);
// Compares two strict three-component numeric release versions.
int compareVersions(const QString& a, const QString& b);
// Quotes a path for a POSIX shell command or returns empty for control characters.
QString shellQuote(const QString& path);

}
