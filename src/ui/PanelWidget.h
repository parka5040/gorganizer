#pragma once

#include <QWidget>
#include "GameInfo.h"

class QTreeView;
class QStandardItemModel;
class QLabel;

namespace gorganizer {

class PanelWidget : public QWidget {
    Q_OBJECT
public:
    explicit PanelWidget(const QString& title, const QString& placeholderText,
                         QWidget* parent = nullptr);

    virtual void loadForGame(const GameInfo& game) = 0;

protected:
    void showContent();
    void showPlaceholder(const QString& text = {});
    void clearModel();

    QTreeView* treeView() const { return m_treeView; }
    QStandardItemModel* model() const { return m_model; }

private:
    QTreeView* m_treeView;
    QStandardItemModel* m_model;
    QLabel* m_placeholder;
    QString m_defaultPlaceholder;
};

} // namespace gorganizer
