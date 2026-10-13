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

// A whole .mmtile file. 0 added (*tile = its ref); -1 bad header, -2 mmap/Detour version,
// -3 truncated, -4 Detour refused the tile.
int nd_add_tile(void* h, const unsigned char* file, int size, unsigned long long* tile);

// Unloads a tile nd_add_tile added. 0 done, -1 Detour refused.
int nd_remove_tile(void* h, unsigned long long tile);

// The nearest walkable point to p within the half extents ext (WoW x/y, then height).
// 0 found (out set), -1 none.
int nd_nearest(void* h, const float* p, const float* ext, unsigned short include, unsigned short exclude,
               float* out);

// The navmesh surfaces at (p[0], p[1]) from height lo to hi, any polygon flags (a falling body
// lands on ground, water, lava and too-steep slopes alike): up to max heights in z and their
// polygon flags in flags. Returns the count, or -1 when Detour fails.
int nd_floors(void* h, const float* p, float lo, float hi, float* z, unsigned short* flags, int max);

// The polygons of every loaded tile whose bounds overlap the box box = {x0, y0, x1, y1} (WoW x/y, x0 <= x1,
// y0 <= y1), for drawing: per polygon n, the polygon flags, then n corners as WoW (x, y, z). Off-mesh connections
// are skipped. Returns the number of floats the answer takes; at most max of them are written to out (out may be
// NULL with max 0 to ask the size). -1 when Detour has no mesh.
int nd_polys(void* h, const float* box, float* out, int max);

// findPath + findStraightPath between the floor points under start and end, as the
// server's PathFinder. 0 complete, 1 partial (out ends at the reachable point nearest the
// goal), 2 partial because the goal is over 7 yd from the mesh, -1 start off the mesh,
// -2 goal off the mesh, -3 Detour failure. *count = points written (3 floats each); goal
// = the floor point found for end (set from 0 up).
int nd_find_path(void* h, const float* start, const float* end, unsigned short include, unsigned short exclude,
                 float* out, int max_points, int* count, float* goal);

#ifdef __cplusplus
}
#endif

#endif
