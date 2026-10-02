#include "InstallLayout.h"

#include <QCoreApplication>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QRegularExpression>
#include <array>
#include <sys/stat.h>
#include <unistd.h>

namespace gorganizer {

namespace {

const QRegularExpression& basePattern()
{
    static const QRegularExpression pattern(QStringLiteral("\\A[0-9]{1,9}\\.[0-9]{1,9}\\.[0-9]{1,9}\\z"));
    return pattern;
}

const QRegularExpression& buildPattern()
{
    static const QRegularExpression pattern(
        QStringLiteral("\\A[0-9]{1,9}\\.[0-9]{1,9}\\.[0-9]{1,9}(\\+[0-9A-Za-z.\\-]{1,64})?\\z"));
    return pattern;
}

bool metadata(const QString& path, struct stat* out)
{
    const QByteArray encoded = QFile::encodeName(path);
    return ::lstat(encoded.constData(), out) == 0;
}

bool secureDirectory(const QString& path, bool exactMode = false)
{
    struct stat st;
    return metadata(path, &st) && S_ISDIR(st.st_mode) && st.st_uid == getuid()
        && (st.st_mode & 0022) == 0 && (!exactMode || (st.st_mode & 07777) == 0755);
}

bool regularFile(const QString& path)
{
    struct stat st;
    return metadata(path, &st) && S_ISREG(st.st_mode);
}

bool secureExecutable(const QString& path)
{
    struct stat st;
    return metadata(path, &st) && S_ISREG(st.st_mode) && st.st_uid == getuid()
        && (st.st_mode & (0022 | S_ISUID | S_ISGID)) == 0
        && (st.st_mode & 0111) != 0 && ::access(QFile::encodeName(path).constData(), X_OK) == 0;
}

bool plainVersion(const QString& version)
{
    return basePattern().match(version).hasMatch();
}

}

AppInstallLayout detectInstallLayout()
{
    AppInstallLayout layout;
    layout.runningVersion = QCoreApplication::applicationVersion();
    if (!buildPattern().match(layout.runningVersion).hasMatch()) {
        layout.kind = InstallKind::Development;
        return layout;
    }
    layout.runningBase = layout.runningVersion.section(QLatin1Char('+'), 0, 0);

    const QString xdg = QString::fromLocal8Bit(qgetenv("XDG_DATA_HOME"));
    layout.dataHome = !xdg.isEmpty() && QDir::isAbsolutePath(xdg)
        ? xdg : QDir::homePath() + QStringLiteral("/.local/share");
    layout.releasesRoot = layout.dataHome + QStringLiteral("/gorganizer/releases");
    const QString appDir = QCoreApplication::applicationDirPath();
    const QString bundle = layout.releasesRoot + QLatin1Char('/') + layout.runningBase;
    const QString canonicalBundle = QFileInfo(bundle).canonicalFilePath();
    if (secureDirectory(layout.dataHome + QStringLiteral("/gorganizer"))
        && secureDirectory(layout.releasesRoot, true)
        && secureDirectory(bundle)
        && secureDirectory(bundle + QStringLiteral("/bin"))
        && !canonicalBundle.isEmpty()
        && QFileInfo(appDir + QStringLiteral("/..")).canonicalFilePath() == canonicalBundle
        && secureExecutable(bundle + QStringLiteral("/gorganizer.sh"))
        && secureExecutable(bundle + QStringLiteral("/bin/gorganizerctl"))
        && regularFile(bundle + QStringLiteral("/release.json"))) {
        layout.kind = InstallKind::Prebuilt;
        layout.bundleDir = bundle;
        layout.scriptPath = bundle + QStringLiteral("/gorganizer.sh");
        return layout;
    }

    const QString source = QFileInfo(appDir + QStringLiteral("/../..")).canonicalFilePath();
    struct stat gitStat;
    if (!source.isEmpty() && regularFile(source + QStringLiteral("/gorganizer.sh"))
        && metadata(source + QStringLiteral("/.git"), &gitStat) && S_ISDIR(gitStat.st_mode)) {
        layout.kind = InstallKind::Source;
        layout.scriptPath = source + QStringLiteral("/gorganizer.sh");
    }
    return layout;
}

bool readInstalledVersion(const QString& releasesRoot, QString* version)
{
    struct stat rootStat;
    if (!metadata(releasesRoot, &rootStat) || !S_ISDIR(rootStat.st_mode))
        return false;
    const QByteArray path = QFile::encodeName(releasesRoot + QStringLiteral("/current"));
    std::array<char, 4096> target{};
    const ssize_t size = ::readlink(path.constData(), target.data(), target.size());
    if (size <= 0 || static_cast<size_t>(size) >= target.size())
        return false;
    const QString name = QString::fromUtf8(target.data(), static_cast<qsizetype>(size));
    if (!plainVersion(name))
        return false;
    struct stat selected;
    if (!metadata(releasesRoot + QLatin1Char('/') + name, &selected)
        || !S_ISDIR(selected.st_mode) || selected.st_uid != getuid())
        return false;
    if (version)
        *version = name;
    return true;
}

int compareVersions(const QString& a, const QString& b)
{
    if (!plainVersion(a) || !plainVersion(b))
        return 0;
    const QStringList left = a.split(QLatin1Char('.'));
    const QStringList right = b.split(QLatin1Char('.'));
    for (int i = 0; i < 3; ++i) {
        const int l = left[i].toInt();
        const int r = right[i].toInt();
        if (l != r)
            return l < r ? -1 : 1;
    }
    return 0;
}

QString shellQuote(const QString& path)
{
    for (QChar c : path) {
        if (c.unicode() < 0x20 || c.unicode() == 0x7f)
            return {};
    }
    QString quoted = path;
    quoted.replace(QLatin1Char('\''), QStringLiteral("'\\''"));
    return QLatin1Char('\'') + quoted + QLatin1Char('\'');
}

}
