cmake_minimum_required(VERSION 3.20)
project(shooter_client CXX C)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

include(FetchContent)

# raylib (render/input/audio)
set(BUILD_EXAMPLES OFF CACHE BOOL "" FORCE)
set(BUILD_GAMES OFF CACHE BOOL "" FORCE)
FetchContent_Declare(raylib
  URL https://github.com/raysan5/raylib/archive/refs/tags/5.5.tar.gz)

# cpp-httplib: header only. SOURCE_SUBDIR points nowhere so only the download happens.
FetchContent_Declare(httplib
  URL https://github.com/yhirose/cpp-httplib/archive/refs/tags/v0.18.3.tar.gz
  SOURCE_SUBDIR none)

FetchContent_MakeAvailable(raylib httplib)

find_package(OpenSSL REQUIRED)
find_package(PkgConfig REQUIRED)
pkg_check_modules(SODIUM REQUIRED IMPORTED_TARGET libsodium)

# Networking/crypto core, testable without raylib.
add_library(client_core STATIC crypto.cpp net.cpp api.cpp)
target_include_directories(client_core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR} ${httplib_SOURCE_DIR})
target_link_libraries(client_core PUBLIC PkgConfig::SODIUM OpenSSL::SSL OpenSSL::Crypto)
if(WIN32)
  target_link_libraries(client_core PUBLIC ws2_32 crypt32)
endif()

add_executable(shooter main.cpp)
target_link_libraries(shooter PRIVATE client_core raylib)

# Keep float math identical to the Go server (no FMA contraction) for prediction.
if(CMAKE_CXX_COMPILER_ID MATCHES "GNU|Clang")
  target_compile_options(client_core PUBLIC -ffp-contract=off -Wall -Wextra)
  target_compile_options(shooter PRIVATE -ffp-contract=off -Wall -Wextra)
endif()

enable_testing()
add_executable(core_test tests/core_test.cpp)
target_link_libraries(core_test PRIVATE client_core)
add_test(NAME core_test COMMAND core_test)
