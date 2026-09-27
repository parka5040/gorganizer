#pragma once

#include "GrpcTypes.h"
#include "gorganizer.pb.h"

namespace gorganizer {

gorganizer::v1::PreviewInstallRequest previewInstallRequest(const QString& gameId,
                                                            const QString& archiveRelPath,
                                                            const QString& externalArchivePath);
GrpcPreviewInstallResult previewInstallResultFromProto(const gorganizer::v1::PreviewInstallResponse& response);

}
