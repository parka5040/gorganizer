#pragma once

#include <QObject>
#include <QStringList>

#include "Palette.h"

namespace gorganizer {

class ThemeManager : public QObject {
    Q_OBJECT
public:
    static ThemeManager* instance();

    // Theme names for the Settings combo and the View -> Theme menu.
    static QStringList availableThemes();

    // Appearance modes for the View -> Appearance menu. {System, Light, Dark}
    static QStringList availableModes();

    // Whether `name` is a known theme (or a legacy alias of one).
    static bool isKnownTheme(const QString& name);

    static QString canonicalThemeName(const QString& name);

    // Apply `themeName` using the current mode.
    static void applyTheme(const QString& themeName);

    // Resolve mode ("system"/"light"/"dark") + theme name and apply the result.
    static void applyMode(const QString& mode, const QString& themeName);

    static bool systemPrefersDark();

    static const Palette& currentPalette();

signals:
    // Emitted after a new Palette is applied (stylesheet + QPalette in place).
    void themeChanged(const Palette& palette);

private:
    explicit ThemeManager(QObject* parent = nullptr);

    static void apply(const QString& mode, const QString& themeName);

    static Palette s_current;
    static QString s_currentMode;
    static bool s_applying;
};

}
