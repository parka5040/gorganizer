#pragma once

#include <QFrame>
#include <QList>
#include <functional>

class QBoxLayout;
class QLabel;
class QToolButton;

namespace gorganizer {

class NoticeBar : public QFrame {
    Q_OBJECT
public:
    enum class Kind { Info, Warning };
    struct Action {
        QString label;
        std::function<void()> run;
    };

    explicit NoticeBar(QWidget* parent = nullptr);
    // Shows a plain-text notice with up to three actions and an optional dismiss button.
    void showNotice(Kind kind, const QString& text, const QList<Action>& actions, bool dismissible);
    // Hides the current notice and clears its actions.
    void clear();
    // Renames a visible action whose current label matches oldLabel.
    void renameAction(const QString& oldLabel, const QString& newLabel);

signals:
    void dismissed();

protected:
    // Stacks the notice actions when horizontal space is limited.
    void resizeEvent(QResizeEvent* event) override;

private:
    // Applies the current theme's colours to the notice.
    void applyPalette();
    // Moves focus out of the notice before hiding it.
    void releaseFocus();
    // Publishes the current text to accessibility clients.
    void updateAccessibleText(const QString& text);

    QLabel* m_label;
    QToolButton* m_close;
    QBoxLayout* m_actions;
    Kind m_kind = Kind::Info;
};

}
