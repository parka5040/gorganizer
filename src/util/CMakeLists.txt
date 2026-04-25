add_library(gorganizer_util STATIC Paths.cpp)
target_include_directories(gorganizer_util PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(gorganizer_util PUBLIC Qt6::Core)
