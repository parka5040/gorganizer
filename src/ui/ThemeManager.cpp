#include "ThemeManager.h"

#include <QApplication>
#include <QFile>
#include <QHash>
#include <QtGlobal>

namespace gorganizer {

QStringList ThemeManager::availableThemes()
{
    return {"Default", "Dracula", "Monokai", "Nord", "Gruvbox Dark", "One Dark", "Solarized Dark"};
}

void ThemeManager::applyTheme(const QString& name)
{
    if (name.isEmpty() || name == "Default") {
        qApp->setStyleSheet(QString());
        return;
    }
    QString qss = qssForTheme(name);
    if (qss.isEmpty()) {
        qWarning("gorganizer: failed to load theme '%s' — falling back to Default", qPrintable(name));
        qApp->setStyleSheet(QString());
        return;
    }
    qApp->setStyleSheet(qss);
}

QString ThemeManager::qssForTheme(const QString& name)
{
    static const QHash<QString, QString> resourceMap = {
        {"Dracula",        ":/themes/dracula.qss"},
        {"Monokai",        ":/themes/monokai.qss"},
        {"Nord",           ":/themes/nord.qss"},
        {"Gruvbox Dark",   ":/themes/gruvbox-dark.qss"},
        {"One Dark",       ":/themes/one-dark.qss"},
        {"Solarized Dark", ":/themes/solarized-dark.qss"},
    };

    auto it = resourceMap.find(name);
    if (it == resourceMap.end())
        return {};

    QFile f(it.value());
    if (!f.open(QIODevice::ReadOnly | QIODevice::Text)) {
        qWarning("gorganizer: theme resource '%s' unavailable — qrc may not be compiled in",
                 qPrintable(it.value()));
        return {};
    }
    return QString::fromUtf8(f.readAll());
}

} // namespace gorganizer
