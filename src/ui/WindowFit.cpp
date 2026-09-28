#include "WindowFit.h"

#include <QGuiApplication>
#include <QScreen>
#include <QTimer>
#include <QWidget>
#include <algorithm>

namespace gorganizer {

namespace {

QScreen* screenFor(QWidget* window)
{
    if (window->parentWidget() && !window->isVisible()) {
        if (QScreen* screen = window->parentWidget()->screen())
            return screen;
    }
    QScreen* best = nullptr;
    int largestOverlap = 0;
    for (QScreen* screen : QGuiApplication::screens()) {
        const QRect overlap = window->frameGeometry().intersected(screen->availableGeometry());
        const int area = overlap.width() * overlap.height();
        if (area > largestOverlap) {
            largestOverlap = area;
            best = screen;
        }
    }
    if (!best && window->parentWidget())
        best = window->parentWidget()->screen();
    return best ? best : QGuiApplication::primaryScreen();
}

}

void fitToScreen(QWidget* window, QSize preferred, QSize minimum)
{
    QScreen* screen = screenFor(window);
    if (!screen) return;
    const QRect available = screen->availableGeometry();
    const QSize limit(std::max(1, available.width() * 95 / 100),
                      std::max(1, available.height() * 95 / 100));
    if (minimum.isValid())
        window->setMinimumSize(minimum.boundedTo(limit));
    window->resize(preferred.boundedTo(limit));
    window->move(available.center() - QPoint(window->width() / 2, window->height() / 2));
    QTimer::singleShot(0, window, [window] {
        if (!window->isVisible()) return;
        clampToScreen(window);
        if (QScreen* shownScreen = screenFor(window)) {
            const QRect frame = window->frameGeometry();
            window->move(shownScreen->availableGeometry().center()
                         - QPoint(frame.width() / 2, frame.height() / 2));
        }
    });
}

void clampToScreen(QWidget* window)
{
    QScreen* screen = screenFor(window);
    if (!screen) return;
    const QRect available = screen->availableGeometry();
    const QSize frameExtra = window->frameGeometry().size() - window->size();
    const QSize limit = (available.size() - frameExtra).expandedTo(QSize(1, 1));
    window->setMinimumSize(window->minimumSize().boundedTo(limit));
    window->resize(window->size().boundedTo(limit));
    QRect frame = window->frameGeometry();
    frame.moveLeft(std::clamp(frame.left(), available.left(),
                              std::max(available.left(), available.right() - frame.width() + 1)));
    frame.moveTop(std::clamp(frame.top(), available.top(),
                             std::max(available.top(), available.bottom() - frame.height() + 1)));
    window->move(frame.topLeft());
}

}
