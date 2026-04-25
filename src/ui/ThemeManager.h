#pragma once

#include <QStringList>

namespace gorganizer {

class ThemeManager {
public:
    // Legacy API: returns the full list (Default + dark variants), still used
    // by the SettingsDialog combo. New code should prefer applyMode().
    static QStringList availableThemes();
    static void applyTheme(const QString& name);

    // Top-level appearance modes the user chooses from.
    // "system" honors the OS color scheme (Qt 6.5+ QStyleHints), falling
    // back to light when unknown.
    static QStringList availableModes();      // {"System", "Light", "Dark"}
    static QStringList availableDarkVariants(); // 6 dark themes
    static bool isDarkVariant(const QString& name);

    // Apply the resolved theme: mode + (when relevant) chosen dark variant.
    // - mode = "system": detects OS scheme, applies light or the variant.
    // - mode = "light":  forces the light theme.
    // - mode = "dark":   applies the named dark variant (default Dracula).
    static void applyMode(const QString& mode, const QString& darkVariant);

    // True if the OS reports a dark color scheme. Returns false when Qt
    // can't determine the scheme — light is the safe default.
    static bool systemPrefersDark();

private:
    static QString qssForTheme(const QString& name);
};

} // namespace gorganizer
