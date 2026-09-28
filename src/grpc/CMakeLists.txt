set(PROTO_SRC_DIR ${CMAKE_SOURCE_DIR}/api/proto)
set(PROTO_OUT_DIR ${CMAKE_CURRENT_BINARY_DIR}/generated)
file(MAKE_DIRECTORY ${PROTO_OUT_DIR})

set(PROTO_FILE ${PROTO_SRC_DIR}/gorganizer.proto)
set(PROTO_SRCS ${PROTO_OUT_DIR}/gorganizer.pb.cc)
set(PROTO_HDRS ${PROTO_OUT_DIR}/gorganizer.pb.h)
set(GRPC_SRCS ${PROTO_OUT_DIR}/gorganizer.grpc.pb.cc)
set(GRPC_HDRS ${PROTO_OUT_DIR}/gorganizer.grpc.pb.h)

# Protobuf and gRPC generators are dependencies so an upgrade regenerates the
# code before stale gencode fails its PROTOBUF_VERSION check.
add_custom_command(
    OUTPUT ${PROTO_SRCS} ${PROTO_HDRS} ${GRPC_SRCS} ${GRPC_HDRS}
    COMMAND ${PROTOC}
        --cpp_out=${PROTO_OUT_DIR}
        --grpc_out=${PROTO_OUT_DIR}
        --plugin=protoc-gen-grpc=${gRPC_CPP_PLUGIN}
        -I${PROTO_SRC_DIR}
        ${PROTO_FILE}
    DEPENDS ${PROTO_FILE} ${PROTOC} ${gRPC_CPP_PLUGIN}
    COMMENT "Generating gRPC C++ sources from gorganizer.proto"
)

add_library(gorganizer_grpc STATIC
    ${PROTO_SRCS}
    ${GRPC_SRCS}
    GrpcClient.cpp
    GrpcTypes.cpp
    GrpcWorker.cpp
)

target_include_directories(gorganizer_grpc PUBLIC
    ${CMAKE_CURRENT_SOURCE_DIR}
    ${PROTO_OUT_DIR}
)

if(GORGANIZER_RELEASE)
    set(GORGANIZER_PROTOBUF_LIB protobuf::libprotobuf)
    set(GORGANIZER_GRPC_LIB gRPC::grpc++)
else()
    set(GORGANIZER_PROTOBUF_LIB PkgConfig::PROTOBUF)
    set(GORGANIZER_GRPC_LIB PkgConfig::GRPC)
endif()
target_link_libraries(gorganizer_grpc PUBLIC
    gorganizer_core
    Qt6::Core
    ${GORGANIZER_PROTOBUF_LIB}
    ${GORGANIZER_GRPC_LIB}
)
