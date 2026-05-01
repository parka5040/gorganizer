#pragma once

#include <QStringList>

namespace gorganizer {

class ThemeManager {
public:
    static QStringList availableThemes();
    static void applyTheme(const QString& name);

    static QStringList availableModes();
    static QStringList availableDarkVariants();
    static bool isDarkVariant(const QString& name);

    // Resolves mode ("system"/"light"/"dark") + dark variant name and applies the resulting theme.
    static void applyMode(const QString& mode, const QString& darkVariant);

    static bool systemPrefersDark();

private:
    static QString qssForTheme(const QString& name);
};

} // namespace gorganizer
