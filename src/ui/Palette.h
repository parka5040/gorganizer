#pragma once

#include <QColor>
#include <QString>

namespace gorganizer {

struct Palette {
    QColor window;
    QColor surface;
    QColor input;
    QColor button;
    QColor alternateBase;

    QColor border;

    QColor text;
    QColor textMuted;

    QColor accent;
    QColor accentText;

    QColor selectionBg;
    QColor selectionText;
    QColor hover;

    QColor disabledBg;
    QColor disabledText;
    QColor disabledBorder;

    QColor scrollHandle;
    QColor scrollHandleHover;

    QColor mid;

    QColor success;
    QColor warning;
    QColor error;
    QColor info;
    QColor successFg;
    QColor warningFg;
    QColor errorFg;
    QColor infoFg;
    QColor successBg;
    QColor warningBg;
    QColor errorBg;
    QColor infoBg;
};

struct Theme {
    QString name;
    Palette light;
    Palette dark;
};

}
