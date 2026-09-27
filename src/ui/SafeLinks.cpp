#include "SafeLinks.h"
#include "Dialogs.h"

#include <QDesktopServices>
#include <QUrl>

namespace gorganizer {

bool openWebLink(QWidget* parent, const QString& url)
{
    const QUrl link(url);
    if (!link.isValid() || link.isRelative() || link.scheme() != QLatin1String("https")
        || link.host().isEmpty() || !link.userInfo().isEmpty()) {
        dialogs::warn(parent, "Link Blocked",
                      "This link cannot be opened safely. Only secure web links (https) are allowed here.");
        return false;
    }
    return QDesktopServices::openUrl(link);
}

}
