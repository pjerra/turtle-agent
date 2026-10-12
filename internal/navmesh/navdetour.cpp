//go:build cgo

// navdetour.cpp - a small C API over the core's Detour (vendored in detour/, zlib) for the
// cgo engine. The calls mirror the server's PathFinder (src/game/Maps/PathFinder.cpp):
// FindWalkPoly's extents and its "not above the point" rule, findPath on the raw start and
// end, then findStraightPath. Ported from wow-play-harness native/navdetour.cpp, without its
// wall margin: a headless client has no collision to catch on corners.
#include <cstdint>
#include <cstring>

#include "DetourAlloc.h"
#include "DetourCommon.h"
#include "DetourNavMesh.h"
#include "DetourNavMeshQuery.h"
#include "navdetour.h"

namespace {
const uint32_t MMAP_MAGIC = 0x4d4d4150;  // 'MMAP'
const uint32_t MMAP_VERSION = 6;         // core MoveMapSharedDefines.h
const int MAX_POLYS = 2048;              // the server keeps 256 (PathFinder.h MAX_PATH_LENGTH)
const int MAX_NODES = 65535;             // the server's query has 2048 (MoveMap.cpp)

struct MmapTileHeader {
    uint32_t mmapMagic, dtVersion, mmapVersion, size, usesLiquids;
};

struct Nav {
    dtNavMesh* mesh = nullptr;
    dtNavMeshQuery* query = nullptr;
};

inline void toDt(const float* w, float* d) { d[0] = w[1]; d[1] = w[2]; d[2] = w[0]; }
inline void toWow(const float* d, float* w) { w[0] = d[2]; w[1] = d[0]; w[2] = d[1]; }

dtQueryFilter makeFilter(unsigned short include, unsigned short exclude) {
    dtQueryFilter f;
    f.setIncludeFlags(include);
    f.setExcludeFlags(exclude);
    return f;
}

// PathInfo::FindWalkPoly: extents 5 yd round, 10 yd of height, and never a point more than
// 3 yd above the asked one.
dtPolyRef findWalkPoly(Nav* n, const float* p, const dtQueryFilter& f, float* closest, float* dist) {
    const float ext[3] = {5.0f, 10.0f, 5.0f};
    dtPolyRef ref = 0;
    if (dtStatusFailed(n->query->findNearestPoly(p, ext, &f, &ref, closest)) || !ref)
        return 0;
    if (closest[1] > p[1] + 3.0f)
        return 0;
    *dist = dtVdist(closest, p);
    return ref;
}
}  // namespace

extern "C" int nd_version(void) { return 100 * DT_NAVMESH_VERSION + (int)sizeof(dtPolyRef); }

extern "C" void* nd_create(const unsigned char* params, int size) {
    if (size < (int)sizeof(dtNavMeshParams))
        return nullptr;
    dtNavMeshParams p;
    std::memcpy(&p, params, sizeof(p));
    Nav* n = new Nav;
    n->mesh = dtAllocNavMesh();
    n->query = dtAllocNavMeshQuery();
    if (!n->mesh || !n->query || dtStatusFailed(n->mesh->init(&p)) ||
        dtStatusFailed(n->query->init(n->mesh, MAX_NODES))) {
        dtFreeNavMeshQuery(n->query);
        dtFreeNavMesh(n->mesh);
        delete n;
        return nullptr;
    }
    return n;
}

extern "C" void nd_destroy(void* h) {
    Nav* n = (Nav*)h;
    if (!n)
        return;
    dtFreeNavMeshQuery(n->query);
    dtFreeNavMesh(n->mesh);
    delete n;
}

extern "C" int nd_add_tile(void* h, const unsigned char* file, int size) {
    Nav* n = (Nav*)h;
    if (!n || size < (int)sizeof(MmapTileHeader))
        return -1;
    MmapTileHeader hd;
    std::memcpy(&hd, file, sizeof(hd));
    if (hd.mmapMagic != MMAP_MAGIC)
        return -1;
    if (hd.mmapVersion != MMAP_VERSION || hd.dtVersion != (uint32_t)DT_NAVMESH_VERSION)
        return -2;
    if (hd.size > (uint32_t)size - sizeof(hd))
        return -3;
    unsigned char* data = (unsigned char*)dtAlloc(hd.size, DT_ALLOC_PERM);
    if (!data)
        return -4;
    std::memcpy(data, file + sizeof(hd), hd.size);
    dtTileRef ref = 0;
    if (dtStatusFailed(n->mesh->addTile(data, (int)hd.size, DT_TILE_FREE_DATA, 0, &ref))) {
        dtFree(data);
        return -4;
    }
    return 0;
}

extern "C" int nd_nearest(void* h, const float* p, const float* ext, unsigned short include,
                          unsigned short exclude, float* out) {
    Nav* n = (Nav*)h;
    dtQueryFilter f = makeFilter(include, exclude);
    float dp[3], dext[3] = {ext[1], ext[2], ext[0]}, c[3];
    toDt(p, dp);
    dtPolyRef ref = 0;
    if (dtStatusFailed(n->query->findNearestPoly(dp, dext, &f, &ref, c)) || !ref)
        return -1;
    toWow(c, out);
    return 0;
}

extern "C" int nd_find_path(void* h, const float* start, const float* end, unsigned short include,
                            unsigned short exclude, float* out, int max_points, int* count) {
    Nav* n = (Nav*)h;
    *count = 0;
    dtQueryFilter f = makeFilter(include, exclude);
    float s[3], e[3], sc[3], ec[3], sdist = 0, edist = 0;
    toDt(start, s);
    toDt(end, e);
    dtPolyRef sref = findWalkPoly(n, s, f, sc, &sdist);
    if (!sref)
        return -1;
    dtPolyRef eref = findWalkPoly(n, e, f, ec, &edist);
    if (!eref)
        return -2;
    int result = 0;
    // BuildPolyPath: a goal over 7 yd from its polygon is clamped to the polygon's edge and
    // the path is incomplete.
    if (edist > 7.0f) {
        float c[3];
        if (dtStatusSucceed(n->query->closestPointOnPolyBoundary(eref, e, c)))
            dtVcopy(e, c);
        result = 1;
    }
    dtPolyRef polys[MAX_POLYS];
    int np = 0;
    dtStatus st = n->query->findPath(sref, eref, s, e, &f, polys, &np, MAX_POLYS);
    if (dtStatusFailed(st) || np == 0)
        return -3;
    if (polys[np - 1] != eref || dtStatusDetail(st, DT_PARTIAL_RESULT))
        result = 1;
    int npts = 0;
    if (dtStatusFailed(n->query->findStraightPath(s, e, polys, np, out, nullptr, nullptr, &npts, max_points)) ||
        npts < 1)
        return -3;
    for (int i = 0; i < npts; ++i) {
        float d[3];
        dtVcopy(d, &out[i * 3]);
        toWow(d, &out[i * 3]);
    }
    *count = npts;
    return result;
}
