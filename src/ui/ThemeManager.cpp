#include "ThemeManager.h"

#include <QApplication>
#include <QGuiApplication>
#include <QHash>
#include <QPalette>
#include <QStyleHints>
#include <QtGlobal>

#include <algorithm>
#include <cmath>
#include <vector>

namespace gorganizer {

// ---------------------------------------------------------------------------
// Color math (WCAG contrast + blending). Kept file-local; the rest of the app
// only ever sees resolved Palette colors.
// ---------------------------------------------------------------------------

static double lin(double c)
{
    return c <= 0.03928 ? c / 12.92 : std::pow((c + 0.055) / 1.055, 2.4);
}

static double relLum(const QColor& c)
{
    return 0.2126 * lin(c.redF()) + 0.7152 * lin(c.greenF()) + 0.0722 * lin(c.blueF());
}

static double contrast(const QColor& a, const QColor& b)
{
    double la = relLum(a), lb = relLum(b);
    double hi = std::max(la, lb), lo = std::min(la, lb);
    return (hi + 0.05) / (lo + 0.05);
}

static QColor mix(const QColor& a, const QColor& b, double t)
{
    return QColor::fromRgbF(a.redF() + (b.redF() - a.redF()) * t,
                            a.greenF() + (b.greenF() - a.greenF()) * t,
                            a.blueF() + (b.blueF() - a.blueF()) * t);
}

// Legible ink to draw on top of an `accent` fill.
static QColor onAccent(const QColor& c)
{
    return relLum(c) > 0.45 ? QColor(0x1b, 0x1b, 0x1f) : QColor(0xff, 0xff, 0xff);
}

// Nudge `fg` lightness away from `bg` until it clears the WCAG ratio, so status
// text stays readable in both light and dark forms.
static QColor ensureContrast(QColor fg, const QColor& bg, double ratio)
{
    if (contrast(fg, bg) >= ratio)
        return fg;
    const bool darken = relLum(bg) > 0.4; // light bg -> darken the ink
    for (int i = 0; i < 100; ++i) {
        float h, s, l, a;
        fg.getHslF(&h, &s, &l, &a);
        if (h < 0)
            h = 0;
        l += darken ? -0.02f : 0.02f;
        if (l <= 0.0f || l >= 1.0f) {
            l = qBound(0.0f, l, 1.0f);
            fg.setHslF(h, s, l, a);
            break;
        }
        fg.setHslF(h, s, l, a);
        if (contrast(fg, bg) >= ratio)
            break;
    }
    return fg;
}

// ---------------------------------------------------------------------------
// Palette construction: a handful of canonical anchors per theme; everything
// else is derived so the two forms stay internally consistent.
// ---------------------------------------------------------------------------

static Palette buildPalette(bool dark, const char* window, const char* surface,
                            const char* input, const char* button, const char* border,
                            const char* text, const char* muted, const char* accent,
                            const char* selectionBg, const char* hover, const char* success,
                            const char* warning, const char* error, const char* info)
{
    Palette p;
    p.window = QColor(window);
    p.surface = QColor(surface);
    p.input = QColor(input);
    p.button = QColor(button);
    p.border = QColor(border);
    p.text = QColor(text);
    p.textMuted = QColor(muted);
    p.accent = QColor(accent);
    p.accentText = onAccent(p.accent);
    p.selectionBg = QColor(selectionBg);
    p.selectionText = (contrast(p.text, p.selectionBg) >= 3.0) ? p.text : onAccent(p.selectionBg);
    p.hover = QColor(hover);
    p.alternateBase = mix(p.window, p.text, 0.03);
    p.disabledBg = p.surface;
    p.disabledText = mix(p.surface, p.text, 0.40);
    p.disabledBorder = mix(p.border, p.window, 0.5);
    p.scrollHandle = p.border;
    p.scrollHandleHover = mix(p.border, p.text, 0.25);
    p.mid = p.scrollHandleHover;

    p.success = QColor(success);
    p.warning = QColor(warning);
    p.error = QColor(error);
    p.info = QColor(info);
    p.successFg = ensureContrast(p.success, p.window, 3.5);
    p.warningFg = ensureContrast(p.warning, p.window, 3.5);
    p.errorFg = ensureContrast(p.error, p.window, 3.5);
    p.infoFg = ensureContrast(p.info, p.window, 3.5);
    const double tint = dark ? 0.22 : 0.16;
    p.successBg = mix(p.window, p.success, tint);
    p.warningBg = mix(p.window, p.warning, tint);
    p.errorBg = mix(p.window, p.error, tint);
    p.infoBg = mix(p.window, p.info, tint);
    return p;
}

// ---------------------------------------------------------------------------
// Theme registry. Canonical hex is anchored from each palette's upstream spec;
// light companions for the dark-only palettes keep the hue and are tuned for
// legibility on a light surface.
// ---------------------------------------------------------------------------

static const std::vector<Theme>& registry()
{
    //                     dark   window     surface    input      button     border     text       muted      accent     selBg      hover      success    warning    error      info
    static const std::vector<Theme> themes = {
        {"Neutral",
         buildPalette(false, "#ffffff", "#f4f5f7", "#ffffff", "#f4f5f7", "#d8dbe1", "#1f2328", "#57606a", "#2f7fe8", "#cfe3ff", "#eaecef", "#2da44e", "#bf8700", "#cf222e", "#2f7fe8"),
         buildPalette(true,  "#1e2124", "#17191c", "#26292e", "#2b2f34", "#3a3f45", "#e6e8eb", "#9aa1a9", "#4a9eff", "#2a4d74", "#2b2f34", "#3fb950", "#d29922", "#f85149", "#58a6ff")},

        {"Dracula",
         buildPalette(false, "#f7f7fb", "#edecf5", "#ffffff", "#edecf5", "#d7d5e8", "#282a36", "#6272a4", "#7c5cc4", "#e3dbf7", "#ecebf5", "#2f8f46", "#b5701f", "#d13b3b", "#2f8aa8"),
         buildPalette(true,  "#282a36", "#21222c", "#44475a", "#44475a", "#44475a", "#f8f8f2", "#6272a4", "#bd93f9", "#44475a", "#383a4a", "#50fa7b", "#ffb86c", "#ff5555", "#8be9fd")},

        {"Monokai",
         buildPalette(false, "#faf9f4", "#efeee6", "#ffffff", "#efeee6", "#e2e0d3", "#272822", "#75715e", "#c01458", "#f6d9e4", "#f0efe7", "#4e9a06", "#c06e00", "#c01458", "#1b7f9e"),
         buildPalette(true,  "#272822", "#1e1f1a", "#49483e", "#49483e", "#49483e", "#f8f8f2", "#75715e", "#f92672", "#49483e", "#3a3b32", "#a6e22e", "#fd971f", "#f92672", "#66d9ef")},

        {"Nord",
         buildPalette(false, "#eceff4", "#e5e9f0", "#ffffff", "#e5e9f0", "#d8dee9", "#2e3440", "#4c566a", "#5e81ac", "#d8dee9", "#e0e5ee", "#6a8352", "#b9942f", "#bf616a", "#5e81ac"),
         buildPalette(true,  "#2e3440", "#3b4252", "#434c5e", "#434c5e", "#434c5e", "#eceff4", "#7f8ca3", "#88c0d0", "#434c5e", "#3b4252", "#a3be8c", "#ebcb8b", "#bf616a", "#81a1c1")},

        {"Gruvbox",
         buildPalette(false, "#fbf1c7", "#ebdbb2", "#fbf1c7", "#ebdbb2", "#d5c4a1", "#3c3836", "#7c6f64", "#076678", "#ebdbb2", "#f2e5bc", "#79740e", "#b57614", "#9d0006", "#076678"),
         buildPalette(true,  "#282828", "#3c3836", "#504945", "#504945", "#504945", "#ebdbb2", "#a89984", "#83a598", "#504945", "#3c3836", "#b8bb26", "#fabd2f", "#fb4934", "#83a598")},

        {"One Dark",
         buildPalette(false, "#fafafa", "#f0f0f1", "#ffffff", "#f0f0f1", "#dbdbdc", "#383a42", "#a0a1a7", "#4078f2", "#d7e4fb", "#ededee", "#50a14f", "#986801", "#e45649", "#0184bc"),
         buildPalette(true,  "#282c34", "#21252b", "#2c313a", "#2c313a", "#4b5263", "#abb2bf", "#5c6370", "#61afef", "#3a3f4b", "#2c313a", "#98c379", "#e5c07b", "#e06c75", "#56b6c2")},

        {"Solarized",
         buildPalette(false, "#fdf6e3", "#eee8d5", "#fdf6e3", "#eee8d5", "#93a1a1", "#657b83", "#93a1a1", "#268bd2", "#eee8d5", "#f5efdc", "#859900", "#b58900", "#dc322f", "#2aa198"),
         buildPalette(true,  "#002b36", "#073642", "#073642", "#073642", "#586e75", "#839496", "#586e75", "#268bd2", "#073642", "#063540", "#859900", "#b58900", "#dc322f", "#2aa198")},
    };
    return themes;
}

// Resolve a theme name (or a legacy alias) to a registry entry; falls back to Neutral.
static const Theme& themeByName(const QString& name)
{
    const auto& themes = registry();
    QString n = name.trimmed();
    // Legacy aliases from older configs / the previous naming.
    if (n.isEmpty() || n.compare("Light", Qt::CaseInsensitive) == 0
        || n.compare("Default", Qt::CaseInsensitive) == 0)
        n = QStringLiteral("Neutral");
    else if (n.compare("Gruvbox Dark", Qt::CaseInsensitive) == 0)
        n = QStringLiteral("Gruvbox");
    else if (n.compare("Solarized Dark", Qt::CaseInsensitive) == 0)
        n = QStringLiteral("Solarized");

    for (const auto& t : themes)
        if (t.name.compare(n, Qt::CaseInsensitive) == 0)
            return t;
    return themes.front(); // Neutral
}

// ---------------------------------------------------------------------------
// QSS generation. The template is a compiled-in string literal referenced by
// apply(), so it can never be stripped by the linker — no Qt resource, no
// Q_INIT_RESOURCE, no way for theming to silently fail to load.
// ---------------------------------------------------------------------------

static QString buildStyleSheet(const Palette& p)
{
    static const char* kTemplate = R"qss(
QWidget { color: %text%; font-size: 13px; }
QMainWindow, QDialog, QWizard, QMessageBox { background-color: %window%; }
QAbstractScrollArea { background-color: %window%; }

QMenuBar { background-color: %surface%; color: %text%; border-bottom: 1px solid %border%; }
QMenuBar::item { background: transparent; padding: 4px 8px; }
QMenuBar::item:selected { background-color: %hover%; }

QMenu { background-color: %window%; color: %text%; border: 1px solid %border%; }
QMenu::item { padding: 4px 24px 4px 20px; }
QMenu::item:selected { background-color: %hover%; }
QMenu::separator { height: 1px; background: %border%; margin: 4px 8px; }

QToolBar { background-color: %surface%; border-bottom: 1px solid %border%; spacing: 4px; padding: 2px; }
QToolButton { background-color: transparent; color: %text%; border: 1px solid transparent; border-radius: 3px; padding: 4px 8px; }
QToolButton:hover { background-color: %hover%; border-color: %border%; }
QToolButton:pressed { background-color: %accent%; color: %accentText%; }
QToolButton:checked { background-color: %hover%; }

QPushButton { background-color: %button%; color: %text%; border: 1px solid %border%; border-radius: 4px; padding: 5px 15px; }
QPushButton:hover { background-color: %hover%; }
QPushButton:pressed { background-color: %accent%; color: %accentText%; }
QPushButton:disabled { background-color: %disBg%; color: %disText%; border-color: %disBorder%; }

QComboBox { background-color: %input%; color: %text%; border: 1px solid %border%; border-radius: 3px; padding: 3px 8px; }
QComboBox:hover { border-color: %accent%; }
QComboBox:disabled { background-color: %disBg%; color: %disText%; border-color: %disBorder%; }
QComboBox::drop-down { border: none; width: 20px; }
QComboBox QAbstractItemView { background-color: %window%; color: %text%; border: 1px solid %border%; selection-background-color: %selBg%; selection-color: %selText%; }

QLineEdit, QPlainTextEdit, QTextEdit { background-color: %input%; color: %text%; border: 1px solid %border%; border-radius: 3px; padding: 4px; selection-background-color: %accent%; selection-color: %accentText%; }
QLineEdit:focus, QPlainTextEdit:focus, QTextEdit:focus { border-color: %accent%; }
QLineEdit:disabled { background-color: %disBg%; color: %disText%; border-color: %disBorder%; }

QAbstractSpinBox { background-color: %input%; color: %text%; border: 1px solid %border%; border-radius: 3px; padding: 3px 4px; }
QAbstractSpinBox:focus { border-color: %accent%; }
QAbstractSpinBox::up-button, QAbstractSpinBox::down-button { background-color: %surface%; border: none; width: 16px; }
QAbstractSpinBox::up-button:hover, QAbstractSpinBox::down-button:hover { background-color: %hover%; }

QTreeView, QListView, QTableView { background-color: %window%; alternate-background-color: %altBase%; color: %text%; border: 1px solid %border%; selection-background-color: %selBg%; selection-color: %selText%; }
QTreeView::item:hover, QListView::item:hover, QTableView::item:hover { background-color: %hover%; }
QTreeView::item:selected, QListView::item:selected, QTableView::item:selected { background-color: %selBg%; color: %selText%; }

QHeaderView::section { background-color: %surface%; color: %accent%; border: none; border-right: 1px solid %border%; border-bottom: 1px solid %border%; padding: 4px 8px; font-weight: bold; }
QHeaderView::section:hover { background-color: %hover%; }

QTabWidget::pane { border: 1px solid %border%; background-color: %window%; }
QTabBar::tab { background-color: %surface%; color: %muted%; border: 1px solid %border%; border-bottom: none; padding: 6px 16px; margin-right: 2px; }
QTabBar::tab:selected { background-color: %window%; color: %text%; border-bottom: 2px solid %accent%; }
QTabBar::tab:hover:!selected { background-color: %hover%; color: %text%; }

QStatusBar { background-color: %surface%; color: %text%; border-top: 1px solid %border%; }
QStatusBar QLabel { color: %text%; background: transparent; }

QSplitter::handle { background-color: %border%; }

QScrollBar:vertical { background-color: %window%; width: 12px; border: none; }
QScrollBar::handle:vertical { background-color: %scroll%; border-radius: 4px; min-height: 20px; margin: 2px; }
QScrollBar::handle:vertical:hover { background-color: %scrollHover%; }
QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical { height: 0; }
QScrollBar:horizontal { background-color: %window%; height: 12px; border: none; }
QScrollBar::handle:horizontal { background-color: %scroll%; border-radius: 4px; min-width: 20px; margin: 2px; }
QScrollBar::handle:horizontal:hover { background-color: %scrollHover%; }
QScrollBar::add-line:horizontal, QScrollBar::sub-line:horizontal { width: 0; }

QProgressBar { background-color: %surface%; border: 1px solid %border%; border-radius: 3px; text-align: center; color: %text%; }
QProgressBar::chunk { background-color: %accent%; border-radius: 2px; }

QDialogButtonBox QPushButton { min-width: 80px; }

QLabel { color: %text%; background: transparent; }
QLabel#hintLabel { color: %muted%; }
QLabel#monoHintLabel { color: %muted%; font-family: monospace; }
QLabel#connectionDot { border-radius: 5px; }
QLabel#connectionDot[connected="true"] { background-color: %success%; }
QLabel#connectionDot[connected="false"] { background-color: %error%; }

/* Only style the label; leave the indicator to Fusion so it still draws a real
 * checkmark/dot (a QSS-styled indicator has no image and can't show one). The
 * synced QPalette themes the native indicator's box, check, and focus ring. */
QCheckBox, QRadioButton { color: %text%; background: transparent; spacing: 6px; }

QGroupBox { border: 1px solid %border%; border-radius: 4px; margin-top: 8px; padding-top: 8px; color: %accent%; font-weight: bold; }
QGroupBox::title { subcontrol-origin: margin; left: 10px; }

QToolTip { background-color: %window%; color: %text%; border: 1px solid %border%; padding: 4px; }
)qss";

    const std::vector<std::pair<const char*, QColor>> tokens = {
        {"%window%", p.window},         {"%surface%", p.surface},
        {"%input%", p.input},           {"%button%", p.button},
        {"%altBase%", p.alternateBase}, {"%border%", p.border},
        {"%text%", p.text},             {"%muted%", p.textMuted},
        {"%accentText%", p.accentText}, {"%accent%", p.accent},
        {"%selBg%", p.selectionBg},     {"%selText%", p.selectionText},
        {"%hover%", p.hover},           {"%disBg%", p.disabledBg},
        {"%disText%", p.disabledText},  {"%disBorder%", p.disabledBorder},
        {"%scrollHover%", p.scrollHandleHover}, {"%scroll%", p.scrollHandle},
        {"%success%", p.success},       {"%error%", p.error},
    };

    QString s = QString::fromUtf8(kTemplate);
    for (const auto& [key, color] : tokens)
        s.replace(QLatin1String(key), color.name(QColor::HexRgb));
    return s;
}

// Push the semantic Palette into QApplication's QPalette so Fusion sub-controls
// (spinbox/scrollbar arrows, checkmarks, focus rings) and palette-based painters
// (DownloadsRowDelegate reads Base/Window/Text/Mid) follow the active theme.
static QPalette toQPalette(const Palette& p)
{
    QPalette q;
    q.setColor(QPalette::Window, p.window);
    q.setColor(QPalette::WindowText, p.text);
    q.setColor(QPalette::Base, p.input);
    q.setColor(QPalette::AlternateBase, p.alternateBase);
    q.setColor(QPalette::Text, p.text);
    q.setColor(QPalette::Button, p.button);
    q.setColor(QPalette::ButtonText, p.text);
    q.setColor(QPalette::BrightText, p.accentText);
    q.setColor(QPalette::Highlight, p.selectionBg);
    q.setColor(QPalette::HighlightedText, p.selectionText);
    q.setColor(QPalette::ToolTipBase, p.window);
    q.setColor(QPalette::ToolTipText, p.text);
    q.setColor(QPalette::PlaceholderText, p.textMuted);
    q.setColor(QPalette::Link, p.accent);
    q.setColor(QPalette::LinkVisited, mix(p.accent, p.textMuted, 0.4));
    q.setColor(QPalette::Mid, p.mid);
    q.setColor(QPalette::Midlight, mix(p.surface, p.window, 0.5));
    q.setColor(QPalette::Dark, mix(p.border, p.text, 0.3));
    q.setColor(QPalette::Shadow, mix(p.border, p.text, 0.6));

    q.setColor(QPalette::Disabled, QPalette::WindowText, p.disabledText);
    q.setColor(QPalette::Disabled, QPalette::Text, p.disabledText);
    q.setColor(QPalette::Disabled, QPalette::ButtonText, p.disabledText);
    q.setColor(QPalette::Disabled, QPalette::Base, p.disabledBg);
    q.setColor(QPalette::Disabled, QPalette::Button, p.disabledBg);
    q.setColor(QPalette::Disabled, QPalette::Highlight, mix(p.selectionBg, p.window, 0.5));
    q.setColor(QPalette::Disabled, QPalette::HighlightedText, p.disabledText);
    return q;
}

// ---------------------------------------------------------------------------
// ThemeManager
// ---------------------------------------------------------------------------

Palette ThemeManager::s_current;
QString ThemeManager::s_currentMode;
bool ThemeManager::s_applying = false;

ThemeManager::ThemeManager(QObject* parent) : QObject(parent) {}

ThemeManager* ThemeManager::instance()
{
    // Intentionally leaked so it outlives QApplication teardown without warning.
    static ThemeManager* inst = new ThemeManager();
    return inst;
}

QStringList ThemeManager::availableThemes()
{
    QStringList names;
    for (const auto& t : registry())
        names << t.name;
    return names;
}

QStringList ThemeManager::availableModes()
{
    return {"System", "Light", "Dark"};
}

QStringList ThemeManager::availableDarkVariants()
{
    return availableThemes();
}

bool ThemeManager::isKnownTheme(const QString& name)
{
    for (const auto& t : registry())
        if (t.name.compare(name, Qt::CaseInsensitive) == 0)
            return true;
    return false;
}

bool ThemeManager::isDarkVariant(const QString& name)
{
    return isKnownTheme(name);
}

QString ThemeManager::canonicalThemeName(const QString& name)
{
    return themeByName(name).name;
}

bool ThemeManager::systemPrefersDark()
{
    if (auto* hints = QGuiApplication::styleHints())
        return hints->colorScheme() == Qt::ColorScheme::Dark;
    return false;
}

const Palette& ThemeManager::currentPalette()
{
    return s_current;
}

void ThemeManager::applyTheme(const QString& themeName)
{
    apply(s_currentMode.isEmpty() ? QStringLiteral("system") : s_currentMode, themeName);
}

void ThemeManager::applyMode(const QString& mode, const QString& themeName)
{
    apply(mode, themeName);
}

void ThemeManager::apply(const QString& mode, const QString& themeName)
{
    // setColorScheme() below re-emits colorSchemeChanged, which main.cpp routes
    // back into applyMode("system", ...). The guard makes that re-entry a no-op.
    if (s_applying)
        return;
    s_applying = true;

    s_currentMode = mode.isEmpty() ? QStringLiteral("system") : mode;
    const Theme& t = themeByName(themeName);

    bool isDark;
    auto* hints = QGuiApplication::styleHints();
    if (s_currentMode == "light") {
        isDark = false;
#if QT_VERSION >= QT_VERSION_CHECK(6, 8, 0)
        if (hints)
            hints->setColorScheme(Qt::ColorScheme::Light);
#endif
    } else if (s_currentMode == "dark") {
        isDark = true;
#if QT_VERSION >= QT_VERSION_CHECK(6, 8, 0)
        if (hints)
            hints->setColorScheme(Qt::ColorScheme::Dark);
#endif
    } else {
#if QT_VERSION >= QT_VERSION_CHECK(6, 8, 0)
        // Clear any prior override so colorScheme() reflects the real OS value.
        if (hints)
            hints->setColorScheme(Qt::ColorScheme::Unknown);
#endif
        isDark = hints && hints->colorScheme() == Qt::ColorScheme::Dark;
    }

    s_current = isDark ? t.dark : t.light;

    if (qApp) {
        qApp->setPalette(toQPalette(s_current));
        qApp->setStyleSheet(buildStyleSheet(s_current));
    }

    s_applying = false;
    emit instance()->themeChanged(s_current);
}

} // namespace gorganizer
