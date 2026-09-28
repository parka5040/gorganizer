#pragma once

#include "GrpcTypes.h"
#include <QString>
#include <optional>

class QWidget;

namespace gorganizer {

struct InstallCollisionChoice {
    GrpcInstallMode mode;
    QString targetMod;
};

QString askInstallModName(QWidget* parent, const QString& title, const QString& label,
                          const QString& initial);
std::optional<InstallCollisionChoice> resolveInstallCollision(QWidget* parent,
                                                               const QString& error,
                                                               const QString& fallbackName);

}
