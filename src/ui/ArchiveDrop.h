#pragma once

#include <QStringList>

class QMimeData;

namespace gorganizer {

struct ArchiveDrop {
    QStringList paths;
    QStringList rejected;
};

ArchiveDrop inspectArchiveDrop(const QMimeData* mime);

}
