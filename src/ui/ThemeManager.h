#pragma once

#include <QObject>
#include <QStringList>

#include "Palette.h"

namespace gorganizer {

// Owns the active theme. Colors are generated in C++ from a semantic Palette
// (see Palette.h) and applied via qApp->setStyleSheet() + qApp->setPalette(),
// so there is no Qt resource to fail to initialize.
//
// The class is a QObject singleton (so it can emit themeChanged), but every
// method used by the rest of the app is static and forwards to the singleton,
// keeping existing call sites unchanged.
class ThemeManager : public QObject {
    Q_OBJECT
public:
    static ThemeManager* instance();

    // Theme names for the Settings combo and the View -> Theme menu.
    // {Neutral, Dracula, Monokai, Nord, Gruvbox, One Dark, Solarized}
    static QStringList availableThemes();

    // Appearance modes for the View -> Appearance menu. {System, Light, Dark}
    static QStringList availableModes();

    // Back-compat: returns availableThemes(). Every theme now has a light and a
    // dark form, so there is no separate "dark variant" list.
    static QStringList availableDarkVariants();

    // Whether `name` is a known theme (or a legacy alias of one).
    static bool isKnownTheme(const QString& name);
    // Back-compat alias kept for existing call sites.
    static bool isDarkVariant(const QString& name);

    // Resolve `name` (including legacy aliases like "Gruvbox Dark"/"Light") to
    // the canonical registry name; falls back to "Neutral". Used so menu/combo
    // selection state matches old stored config values.
    static QString canonicalThemeName(const QString& name);

    // Apply `themeName` using the current mode.
    static void applyTheme(const QString& themeName);

    // Resolve mode ("system"/"light"/"dark") + theme name and apply the result.
    static void applyMode(const QString& mode, const QString& themeName);

    static bool systemPrefersDark();

    // The Palette currently applied. Delegates/models/painters read status and
    // muted colors from here.
    static const Palette& currentPalette();

signals:
    // Emitted after a new Palette is applied (stylesheet + QPalette in place).
    // Widgets that bake token colors into pixmaps/models/HTML subscribe to this.
    void themeChanged(const Palette& palette);

private:
    explicit ThemeManager(QObject* parent = nullptr);

    static void apply(const QString& mode, const QString& themeName);

    static Palette s_current;
    static QString s_currentMode;
    static bool s_applying;
};

} // namespace gorganizer
