#include "ThemeManager.h"

#include <QApplication>
#include <QFile>
#include <QGuiApplication>
#include <QHash>
#include <QStyleHints>
#include <QtGlobal>

namespace gorganizer {

QStringList ThemeManager::availableThemes()
{
    return {"Light", "Dracula", "Monokai", "Nord", "Gruvbox Dark", "One Dark", "Solarized Dark"};
}

QStringList ThemeManager::availableModes()
{
    return {"System", "Light", "Dark"};
}

QStringList ThemeManager::availableDarkVariants()
{
    return {"Dracula", "Monokai", "Nord", "Gruvbox Dark", "One Dark", "Solarized Dark"};
}

bool ThemeManager::isDarkVariant(const QString& name)
{
    return availableDarkVariants().contains(name);
}

bool ThemeManager::systemPrefersDark()
{
    if (auto* hints = QGuiApplication::styleHints())
        return hints->colorScheme() == Qt::ColorScheme::Dark;
    return false;
}

void ThemeManager::applyTheme(const QString& name)
{
    if (name.isEmpty() || name == "Default" || name == "Light") {
        QString qss = qssForTheme("Light");
        qApp->setStyleSheet(qss);
        return;
    }
    QString qss = qssForTheme(name);
    if (qss.isEmpty()) {
        qWarning("gorganizer: failed to load theme '%s' — falling back to Light", qPrintable(name));
        qApp->setStyleSheet(qssForTheme("Light"));
        return;
    }
    qApp->setStyleSheet(qss);
}

void ThemeManager::applyMode(const QString& mode, const QString& darkVariant)
{
    QString variant = isDarkVariant(darkVariant) ? darkVariant : QStringLiteral("Dracula");

    if (mode == "light") {
        applyTheme("Light");
        return;
    }
    if (mode == "dark") {
        applyTheme(variant);
        return;
    }
    applyTheme(systemPrefersDark() ? variant : QStringLiteral("Light"));
}

QString ThemeManager::qssForTheme(const QString& name)
{
    static const QHash<QString, QString> resourceMap = {
        {"Light",          ":/themes/light.qss"},
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
