#pragma once

#include <QStringList>

namespace gorganizer {

class ThemeManager {
public:
    static QStringList availableThemes();
    static void applyTheme(const QString& name);

private:
    static QString qssForTheme(const QString& name);
};

} // namespace gorganizer
