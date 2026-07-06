#pragma once

#include <QColor>
#include <QString>

namespace gorganizer {

// The single source of truth for every color the UI can render.
//
// A Palette holds one QColor per *semantic* role. Widgets, delegates, models,
// and the generated stylesheet all resolve their colors through these roles
// instead of hardcoding hex values, so the whole app tracks the active theme
// (and inverts cleanly between light and dark) from one place.
struct Palette {
    // --- Surfaces (recessed -> raised) ---
    QColor window;         // app / dialog / view / menu / tooltip background
    QColor surface;        // "chrome" bars: menubar, toolbar, statusbar, header, unselected tab
    QColor input;          // editable fields: line edit / combo / spinbox / text edit
    QColor button;         // QPushButton resting background
    QColor alternateBase;  // alternating list/tree rows

    QColor border;         // every 1px separator / border / splitter handle

    // --- Foreground ---
    QColor text;           // primary foreground
    QColor textMuted;      // secondary foreground: hints, comments, inactive tabs

    // --- Accent ---
    QColor accent;         // focus ring, checked indicator, progress chunk, tab underline,
                           // header/groupbox titles, :pressed fill
    QColor accentText;     // text/icon drawn on an `accent` fill

    // --- Selection / interaction ---
    QColor selectionBg;    // selected view-item background
    QColor selectionText;  // text on selectionBg
    QColor hover;          // hover background (menu / button / tab / header / view item)

    // --- Disabled ---
    QColor disabledBg;
    QColor disabledText;
    QColor disabledBorder;

    // --- Scrollbars ---
    QColor scrollHandle;
    QColor scrollHandleHover;

    // Maps to QPalette::Mid. DownloadsRowDelegate uses it for the progress-track
    // outline and the Uninstalled/Unknown chip; plain QSS never sets Mid, which is
    // exactly why that delegate looks wrong today.
    QColor mid;

    // --- Semantic status, three tiers ---
    // solid: progress-chunk fills, chips, the connection dot
    QColor success;
    QColor warning;
    QColor error;
    QColor info;
    // text tier: legible on `window` (model foregrounds, conflict markers,
    // plugin-type text, HTML severity spans)
    QColor successFg;
    QColor warningFg;
    QColor errorFg;
    QColor infoFg;
    // tint tier: low-alpha backgrounds (conflict highlights, plugin dep-issue rows)
    QColor successBg;
    QColor warningBg;
    QColor errorBg;
    QColor infoBg;
};

// A named theme provides both a light and a dark rendering. The active
// appearance mode (system/light/dark) selects which Palette is applied.
struct Theme {
    QString name;
    Palette light;
    Palette dark;
};

} // namespace gorganizer
