set(PROTO_SRC_DIR ${CMAKE_SOURCE_DIR}/api/proto)
set(PROTO_OUT_DIR ${CMAKE_CURRENT_BINARY_DIR}/generated)
file(MAKE_DIRECTORY ${PROTO_OUT_DIR})

set(PROTO_FILE ${PROTO_SRC_DIR}/gorganizer.proto)
set(PROTO_SRCS ${PROTO_OUT_DIR}/gorganizer.pb.cc)
set(PROTO_HDRS ${PROTO_OUT_DIR}/gorganizer.pb.h)
set(GRPC_SRCS ${PROTO_OUT_DIR}/gorganizer.grpc.pb.cc)
set(GRPC_HDRS ${PROTO_OUT_DIR}/gorganizer.grpc.pb.h)

add_custom_command(
    OUTPUT ${PROTO_SRCS} ${PROTO_HDRS} ${GRPC_SRCS} ${GRPC_HDRS}
    COMMAND ${PROTOC}
        --cpp_out=${PROTO_OUT_DIR}
        --grpc_out=${PROTO_OUT_DIR}
        --plugin=protoc-gen-grpc=${gRPC_CPP_PLUGIN}
        -I${PROTO_SRC_DIR}
        ${PROTO_FILE}
    DEPENDS ${PROTO_FILE}
    COMMENT "Generating gRPC C++ sources from gorganizer.proto"
)

add_library(gorganizer_grpc STATIC
    ${PROTO_SRCS}
    ${GRPC_SRCS}
    GrpcClient.cpp
    GrpcWorker.cpp
)

target_include_directories(gorganizer_grpc PUBLIC
    ${CMAKE_CURRENT_SOURCE_DIR}
    ${PROTO_OUT_DIR}
)

target_link_libraries(gorganizer_grpc PUBLIC
    Qt6::Core
    PkgConfig::PROTOBUF
    PkgConfig::GRPC
)
