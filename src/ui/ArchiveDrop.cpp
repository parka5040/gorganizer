#include "ArchiveDrop.h"

#include <QFileInfo>
#include <QMimeData>
#include <QUrl>

namespace gorganizer {

ArchiveDrop inspectArchiveDrop(const QMimeData* mime)
{
    ArchiveDrop result;
    if (!mime || !mime->hasUrls())
        return result;

    for (const QUrl& url : mime->urls()) {
        const QString path = url.toLocalFile();
        const QFileInfo source(path);
        QString name = url.isLocalFile() ? source.fileName() : url.fileName();
        if (name.isEmpty())
            name = url.toDisplayString(QUrl::RemoveQuery | QUrl::RemoveFragment | QUrl::RemoveUserInfo);

        QString reason;
        if (!url.isLocalFile()) {
            reason = QStringLiteral("not a file on this computer");
        } else if (source.isDir()) {
            reason = QStringLiteral("folders cannot be installed");
        } else {
            const QString canonical = source.canonicalFilePath();
            const QFileInfo file(canonical);
            if (canonical.isEmpty() || !file.isFile() || !file.isReadable()) {
                reason = QStringLiteral("not a file on this computer");
            } else if (source.suffix().compare(QLatin1String("zip"), Qt::CaseInsensitive) != 0
                       && source.suffix().compare(QLatin1String("7z"), Qt::CaseInsensitive) != 0
                       && source.suffix().compare(QLatin1String("rar"), Qt::CaseInsensitive) != 0) {
                reason = QStringLiteral("not a supported archive (.zip, .7z or .rar)");
            } else {
                result.paths.append(canonical);
            }
        }
        if (!reason.isEmpty())
            result.rejected.append(QStringLiteral("%1: %2").arg(name, reason));
    }
    return result;
}

}
