cmake_minimum_required(VERSION 3.21)
project(gorganizer VERSION 0.1.0 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_AUTOMOC ON)
set(CMAKE_AUTORCC ON)

# Version string surfaced into the binary (--version, About dialog).
# CI sets this from the git tag; local builds default to "dev".
set(GORGANIZER_VERSION "dev" CACHE STRING "Version string baked into the GUI binary")
add_compile_definitions(GORGANIZER_VERSION="${GORGANIZER_VERSION}")

find_package(Qt6 REQUIRED COMPONENTS Core Gui Widgets Network)
find_package(PkgConfig REQUIRED)
pkg_check_modules(PROTOBUF REQUIRED IMPORTED_TARGET protobuf)
pkg_check_modules(GRPC REQUIRED IMPORTED_TARGET grpc++)
find_program(PROTOC protoc REQUIRED)
find_program(gRPC_CPP_PLUGIN grpc_cpp_plugin REQUIRED)

add_subdirectory(src)
