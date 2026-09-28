#pragma once

#include "GrpcTypes.h"

#include <grpcpp/support/status.h>

namespace gorganizer {

inline GrpcError grpcErrorFromStatus(const grpc::Status& status, const QString& method)
{
    return {static_cast<int>(status.error_code()), method, QString::fromStdString(status.error_message())};
}

}
