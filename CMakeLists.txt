cmake_minimum_required(VERSION 3.21)
project(gorganizer VERSION 0.1.0 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_AUTOMOC ON)
set(CMAKE_AUTORCC ON)

# Version string surfaced into the binary (--version, About dialog).
# CI sets this from the git tag; local builds default to "dev".
set(GORGANIZER_VERSION "dev" CACHE STRING "Version string baked into the GUI binary")
# Reject characters that would break out of the C++ string literal in
# the resulting `-DGORGANIZER_VERSION="..."` flag. Pure paranoia for
# the local-dev path; CI sets this from a git tag, which can't contain
# these. Without the check a malicious or simply garbled CACHE entry
# could inject arbitrary code into the compile flag.
if(GORGANIZER_VERSION MATCHES "[\"\\\\\n\r]")
    message(FATAL_ERROR
        "GORGANIZER_VERSION contains forbidden characters (quote, backslash, newline): "
        "${GORGANIZER_VERSION}")
endif()
add_compile_definitions(GORGANIZER_VERSION="${GORGANIZER_VERSION}")

find_package(Qt6 REQUIRED COMPONENTS Core Gui Widgets Network)
find_package(PkgConfig REQUIRED)
pkg_check_modules(PROTOBUF REQUIRED IMPORTED_TARGET protobuf)
pkg_check_modules(GRPC REQUIRED IMPORTED_TARGET grpc++)
find_program(PROTOC protoc REQUIRED)
find_program(gRPC_CPP_PLUGIN grpc_cpp_plugin REQUIRED)

add_subdirectory(src)
