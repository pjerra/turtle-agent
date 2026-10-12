//go:build cgo

// Compiles the vendored Detour source below into this cgo package (cgo only builds the
// package directory, not detour/). The source is unchanged; see detour/License.txt.
#include "detour/Source/DetourCommon.cpp"
