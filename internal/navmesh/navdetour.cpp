//go:build cgo

// navdetour.cpp - a small C API over the core's Detour (vendored in detour/, zlib) for the
// cgo engine. The calls mirror the server's PathFinder (src/game/Maps/PathFinder.cpp):
// FindWalkPoly's extents with a floor-first choice of polygon (see findWalkPoly), findPath, then findStraightPath.
// Unlike the server, both run between the floor points findNearestPoly found, not the asked
// start and end (see nd_find_path). Ported from wow-play-harness native/navdetour.cpp, without its
// wall margin: a headless client has no collision to catch on corners.
#include <cmath>
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

// PathInfo::FindWalkPoly's extents (5 yd round, 10 yd of height) with a stricter choice of polygon. Detour's findNearestPoly
// takes the nearest polygon in any direction, so on stacked floors (a pit under a ledge) a point a few yards under the ledge could snap
// up onto it, and the walk was then planned from, and sent to, the wrong floor. A walker stands on the floor at its feet or below:
// the polygon chosen is the one closest in height among those at the point's height (up to WALK_STEP above, for slopes, detail error and
// noise) and down to WALK_NEAR below it, then one within WALK_HEAD above (a step up), then, below WALK_NEAR, the nearest of the
// floors further down. A polygon over the point's head (more than WALK_HEAD above) is never taken; the server's own rule allowed 3 yd.
const float WALK_STEP = 1.0f;  // above the point, still "at its feet" (a mesh surface up to a yard over a point is the ground it stands on)
const float WALK_NEAR = 2.0f;  // below the point, still "its floor"
const float WALK_HEAD = 2.0f;  // above the point, the most a polygon may be over it

dtPolyRef findWalkPoly(Nav* n, const float* p, const dtQueryFilter& f, float* closest, float* dist) {
    const float ext[3] = {5.0f, 10.0f, 5.0f};
    const int MAX_CAND = 256;
    dtPolyRef refs[MAX_CAND];
    int nrefs = 0;
    if (dtStatusFailed(n->query->queryPolygons(p, ext, &f, refs, &nrefs, MAX_CAND)) || nrefs == 0)
        return 0;
    // Tiers, best first: at the feet or just below, a step up within the head, far below.
    dtPolyRef best = 0;
    int bestTier = 4;
    float bestD = 0;
    for (int i = 0; i < nrefs; ++i) {
        float c[3];
        bool over = false;
        if (dtStatusFailed(n->query->closestPointOnPoly(refs[i], p, c, &over)))
            continue;
        float dy = c[1] - p[1];
        int tier;
        if (dy > WALK_HEAD)
            continue;  // over its head
        else if (dy <= WALK_STEP && dy >= -WALK_NEAR)
            tier = 0;
        else if (dy > WALK_STEP)
            tier = 1;
        else
            tier = 2;
        // Detour's own score: a point directly over a polygon within the climb height of it counts as on it.
        float d = dtVdistSqr(c, p);
        const dtMeshTile* tile = nullptr;
        const dtPoly* poly = nullptr;
        if (over && dtStatusSucceed(n->mesh->getTileAndPolyByRef(refs[i], &tile, &poly))) {
            d = std::fabs(dy) - tile->header->walkableClimb;
            d = d > 0 ? d * d : 0;
        }
        if (tier < bestTier || (tier == bestTier && d < bestD)) {
            best = refs[i];
            bestTier = tier;
            bestD = d;
            dtVcopy(closest, c);
        }
    }
    if (!best)
        return 0;
    *dist = dtVdist(closest, p);
    return best;
}
// The bytes dtNavMesh::addTile reads for a tile with this header (DetourNavMesh.cpp
// addTile's layout), so a header that claims more than the file holds is refused before
// Detour reads past the end.
uint64_t tileDataSize(const unsigned char* data) {
    auto a4 = [](uint64_t x) { return (x + 3) & ~(uint64_t)3; };
    dtMeshHeader h;
    std::memcpy(&h, data, sizeof(h));
    if (h.polyCount < 0 || h.vertCount < 0 || h.maxLinkCount < 0 || h.detailMeshCount < 0 ||
        h.detailVertCount < 0 || h.detailTriCount < 0 || h.bvNodeCount < 0 || h.offMeshConCount < 0)
        return UINT64_MAX;
    return (uint64_t)a4(sizeof(dtMeshHeader)) + a4(sizeof(float) * 3 * (uint64_t)h.vertCount) +
           a4(sizeof(dtPoly) * (uint64_t)h.polyCount) + a4(sizeof(dtLink) * (uint64_t)h.maxLinkCount) +
           a4(sizeof(dtPolyDetail) * (uint64_t)h.detailMeshCount) +
           a4(sizeof(float) * 3 * (uint64_t)h.detailVertCount) +
           a4(sizeof(unsigned char) * 4 * (uint64_t)h.detailTriCount) +
           a4(sizeof(dtBVNode) * (uint64_t)h.bvNodeCount) +
           a4(sizeof(dtOffMeshConnection) * (uint64_t)h.offMeshConCount);
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

extern "C" int nd_add_tile(void* h, const unsigned char* file, int size, unsigned long long* tile) {
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
    if (hd.size < sizeof(dtMeshHeader) || tileDataSize(file + sizeof(hd)) > hd.size)
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
    *tile = ref;
    return 0;
}

extern "C" int nd_remove_tile(void* h, unsigned long long tile) {
    Nav* n = (Nav*)h;
    return dtStatusFailed(n->mesh->removeTile((dtTileRef)tile, nullptr, nullptr)) ? -1 : 0;
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

extern "C" int nd_floors(void* h, const float* p, float lo, float hi, float* z, unsigned short* flags, int max) {
    Nav* n = (Nav*)h;
    dtQueryFilter f = makeFilter(0xffff, 0);
    // A thin column round the point: every polygon whose bounds overlap it, then the
    // surface height of each one the point lies over.
    float w[3] = {p[0], p[1], (lo + hi) / 2}, c[3];
    toDt(w, c);
    const float ext[3] = {0.05f, (hi - lo) / 2 + 0.5f, 0.05f};
    dtPolyRef refs[64];
    int nrefs = 0;
    if (dtStatusFailed(n->query->queryPolygons(c, ext, &f, refs, &nrefs, 64)))
        return -1;
    int count = 0;
    for (int i = 0; i < nrefs && count < max; ++i) {
        float y = 0;
        if (dtStatusFailed(n->query->getPolyHeight(refs[i], c, &y)) || y < lo || y > hi)
            continue;
        unsigned short fl = 0;
        n->mesh->getPolyFlags(refs[i], &fl);
        z[count] = y;
        flags[count] = fl;
        ++count;
    }
    return count;
}

extern "C" int nd_polys(void* h, const float* box, float* out, int max) {
    Nav* n = (Nav*)h;
    if (!n || !n->mesh)
        return -1;
    long total = 0;
    const dtNavMesh* mesh = n->mesh;
    for (int ti = 0; ti < mesh->getMaxTiles(); ++ti) {
        const dtMeshTile* t = mesh->getTile(ti);
        if (!t || !t->header)
            continue;
        // Detour's bounds are (y, z, x): the tile's WoW box is x from bmin[2] to bmax[2], y from bmin[0] to bmax[0].
        if (t->header->bmax[2] < box[0] || t->header->bmin[2] > box[2] || t->header->bmax[0] < box[1] ||
            t->header->bmin[0] > box[3])
            continue;
        for (int pi = 0; pi < t->header->polyCount; ++pi) {
            const dtPoly* p = &t->polys[pi];
            if (p->getType() != DT_POLYTYPE_GROUND || p->vertCount < 3)
                continue;
            float lo[2] = {1e30f, 1e30f}, hi[2] = {-1e30f, -1e30f};
            for (int k = 0; k < p->vertCount; ++k) {
                const float* v = &t->verts[p->verts[k] * 3];
                lo[0] = v[2] < lo[0] ? v[2] : lo[0];
                hi[0] = v[2] > hi[0] ? v[2] : hi[0];
                lo[1] = v[0] < lo[1] ? v[0] : lo[1];
                hi[1] = v[0] > hi[1] ? v[0] : hi[1];
            }
            if (hi[0] < box[0] || lo[0] > box[2] || hi[1] < box[1] || lo[1] > box[3])
                continue;
            long need = 2 + 3L * p->vertCount;
            if (out && total + need <= max) {
                float* o = out + total;
                o[0] = (float)p->vertCount;
                o[1] = (float)p->flags;
                for (int k = 0; k < p->vertCount; ++k)
                    toWow(&t->verts[p->verts[k] * 3], o + 2 + 3 * k);
            }
            total += need;
        }
    }
    return (int)total;
}

extern "C" int nd_raycast(void* h, const float* start, const float* end, unsigned short include,
                          unsigned short exclude, float* t) {
    Nav* n = (Nav*)h;
    if (!n || !n->mesh || !n->query || !t)
        return -3;
    *t = 0;
    dtQueryFilter f = makeFilter(include, exclude);
    float s[3], e[3], sc[3], sdist = 0, normal[3] = {0, 0, 0};
    toDt(start, s);
    toDt(end, e);
    dtPolyRef sref = findWalkPoly(n, s, f, sc, &sdist);
    if (!sref)
        return -1;
    // The ray runs from the floor point under start; Detour reads only the horizontal part of end.
    const int maxPath = 256;
    dtPolyRef visited[maxPath];
    int visitedCount = 0;
    float hit = 0;
    if (dtStatusFailed(n->query->raycast(sref, sc, e, &f, &hit, normal, visited, &visitedCount, maxPath)))
        return -3;
    *t = hit;
    return 0;
}

extern "C" int nd_find_path(void* h, const float* start, const float* end, unsigned short include,
                            unsigned short exclude, float* out, int max_points, int* count, float* goal) {
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
    toWow(ec, goal);
    // The path runs between the points on the floor (findNearestPoly's closest points on
    // the detail surface), not the asked ones: findStraightPath keeps an end inside its
    // polygon as it is, height and all, and a walk to a point yards above or below the
    // floor trips the anticheat's z speed check. BuildPolyPath's rule stays: a goal over
    // 7 yd from the mesh makes the path incomplete.
    int result = edist > 7.0f ? 2 : 0;
    dtPolyRef polys[MAX_POLYS];
    int np = 0;
    dtStatus st = n->query->findPath(sref, eref, sc, ec, &f, polys, &np, MAX_POLYS);
    if (dtStatusFailed(st) || np == 0)
        return -3;
    if (polys[np - 1] != eref || dtStatusDetail(st, DT_PARTIAL_RESULT))
        result = 1;
    int npts = 0;
    if (dtStatusFailed(n->query->findStraightPath(sc, ec, polys, np, out, nullptr, nullptr, &npts, max_points)) ||
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
