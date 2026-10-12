// navdetour.h - a small C API over the core's Detour for the cgo engine (engine_cgo.go).
// Coordinates in and out are WoW (x, y, z); Detour's own are (y, z, x).
#ifndef TURTLE_AGENT_NAVDETOUR_H
#define TURTLE_AGENT_NAVDETOUR_H

#ifdef __cplusplus
extern "C" {
#endif

// 100 * DT_NAVMESH_VERSION + sizeof(dtPolyRef): 708 is the core's build (Detour 7, 64-bit refs).
int nd_version(void);

// params: the map's NNN.mmap file, a raw dtNavMeshParams (28 bytes). NULL when Detour refuses it.
void* nd_create(const unsigned char* params, int size);
void nd_destroy(void* h);

// A whole .mmtile file. 0 added; -1 bad header, -2 mmap/Detour version, -3 truncated,
// -4 Detour refused the tile.
int nd_add_tile(void* h, const unsigned char* file, int size);

// The nearest walkable point to p within the half extents ext (WoW x/y, then height).
// 0 found (out set), -1 none.
int nd_nearest(void* h, const float* p, const float* ext, unsigned short include, unsigned short exclude,
               float* out);

// findPath + findStraightPath from start to end, as the server's PathFinder. 0 complete,
// 1 partial (out ends at the reachable point nearest the goal), -1 start off the mesh,
// -2 goal off the mesh, -3 Detour failure. *count = points written (3 floats each).
int nd_find_path(void* h, const float* start, const float* end, unsigned short include, unsigned short exclude,
                 float* out, int max_points, int* count);

#ifdef __cplusplus
}
#endif

#endif
