#include "NoticeBar.h"
#include "ThemeManager.h"

#include <QAccessible>
#include <QApplication>
#include <QBoxLayout>
#include <QLabel>
#include <QMainWindow>
#include <QPushButton>
#include <QResizeEvent>
#include <QToolButton>

namespace gorganizer {

NoticeBar::NoticeBar(QWidget* parent)
    : QFrame(parent)
    , m_label(new QLabel(this))
    , m_close(new QToolButton(this))
    , m_actions(new QBoxLayout(QBoxLayout::LeftToRight))
{
    setObjectName(QStringLiteral("updateNotice"));
    setFocusPolicy(Qt::NoFocus);
    setAttribute(Qt::WA_ShowWithoutActivating);
    m_label->setTextFormat(Qt::PlainText);
    m_label->setWordWrap(true);
    m_label->setTextInteractionFlags(Qt::TextSelectableByMouse);
    m_label->setSizePolicy(QSizePolicy::Expanding, QSizePolicy::Preferred);
    m_close->setText(QStringLiteral("✕"));
    m_close->setAccessibleName(QStringLiteral("Dismiss"));
    m_close->setToolTip(QStringLiteral("Dismiss"));
    connect(m_close, &QToolButton::clicked, this, [this] {
        clear();
        emit dismissed();
    });

    auto* root = new QVBoxLayout(this);
    root->setContentsMargins(12, 8, 12, 8);
    auto* top = new QHBoxLayout;
    top->addWidget(m_label, 1);
    top->addWidget(m_close, 0, Qt::AlignTop);
    root->addLayout(top);
    root->addLayout(m_actions);
    m_actions->setContentsMargins(0, 0, 0, 0);
    m_actions->setSpacing(8);
    connect(ThemeManager::instance(), &ThemeManager::themeChanged, this,
            [this](const Palette&) { applyPalette(); });
    applyPalette();
    hide();
}

void NoticeBar::showNotice(Kind kind, const QString& text, const QList<Action>& actions, bool dismissible)
{
    releaseFocus();
    m_kind = kind;
    m_label->setText(text);
    while (QLayoutItem* item = m_actions->takeAt(0)) {
        if (QWidget* widget = item->widget()) {
            widget->hide();
            widget->deleteLater();
        }
        delete item;
    }
    for (const Action& action : actions.mid(0, 3)) {
        auto* button = new QPushButton(action.label, this);
        connect(button, &QPushButton::clicked, this, [run = action.run] {
            if (run) run();
        });
        m_actions->addWidget(button);
    }
    m_close->setVisible(dismissible);
    m_actions->setDirection(width() < 500 ? QBoxLayout::TopToBottom : QBoxLayout::LeftToRight);
    m_actions->invalidate();
    applyPalette();
    updateAccessibleText(text);
    show();
    releaseFocus();
}

void NoticeBar::clear()
{
    releaseFocus();
    hide();
    m_label->clear();
    while (QLayoutItem* item = m_actions->takeAt(0)) {
        if (QWidget* widget = item->widget()) {
            widget->hide();
            widget->deleteLater();
        }
        delete item;
    }
    updateAccessibleText(QString());
}

void NoticeBar::renameAction(const QString& oldLabel, const QString& newLabel)
{
    for (int i = 0; i < m_actions->count(); ++i) {
        auto* button = qobject_cast<QPushButton*>(m_actions->itemAt(i)->widget());
        if (button && button->text() == oldLabel) {
            button->setText(newLabel);
            return;
        }
    }
}

void NoticeBar::resizeEvent(QResizeEvent* event)
{
    QFrame::resizeEvent(event);
    const auto direction = width() < 500 ? QBoxLayout::TopToBottom : QBoxLayout::LeftToRight;
    if (m_actions->direction() != direction)
        m_actions->setDirection(direction);
}

void NoticeBar::applyPalette()
{
    const Palette& pal = ThemeManager::currentPalette();
    const QColor bg = m_kind == Kind::Info ? pal.infoBg : pal.warningBg;
    const QColor fg = m_kind == Kind::Info ? pal.infoFg : pal.warningFg;
    setStyleSheet(QStringLiteral("QFrame#updateNotice { background: %1; color: %2; } "
                                 "QFrame#updateNotice QLabel { color: %2; } "
                                 "QFrame#updateNotice QPushButton, QFrame#updateNotice QToolButton { color: %2; }")
                      .arg(bg.name(), fg.name()));
}

void NoticeBar::releaseFocus()
{
    QWidget* focused = QApplication::focusWidget();
    if (!focused || (focused != this && !isAncestorOf(focused)))
        return;
    if (auto* main = qobject_cast<QMainWindow*>(window())) {
        if (QWidget* central = main->centralWidget())
            central->setFocus(Qt::OtherFocusReason);
    }
}

void NoticeBar::updateAccessibleText(const QString& text)
{
    setAccessibleName(text);
    setAccessibleDescription(text);
    QAccessibleEvent event(this, QAccessible::NameChanged);
    QAccessible::updateAccessibility(&event);
}

}
