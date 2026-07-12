#pragma once

#include <grpcpp/grpcpp.h>
#include <chrono>

namespace gorganizer {

constexpr auto kDefaultUnaryTimeout = std::chrono::seconds(30);

// Bounds a unary RPC with an absolute deadline (I-16); streams are deliberately unbounded.
inline void setUnaryDeadline(grpc::ClientContext& ctx,
                             std::chrono::milliseconds timeout = kDefaultUnaryTimeout)
{
    ctx.set_deadline(std::chrono::system_clock::now() + timeout);
}

}
