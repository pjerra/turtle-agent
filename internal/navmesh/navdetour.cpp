//go:build cgo

// navdetour.cpp - a small C API over the core's Detour (vendored in detour/, zlib) for the
// cgo engine. The calls mirror the server's PathFinder (src/game/Maps/PathFinder.cpp):
// FindWalkPoly's extents and its "not above the point" rule, findPath, then findStraightPath.
// Unlike the server, both run between the floor points findNearestPoly found, not the asked
// start and end (see nd_find_path). Ported from wow-play-harness native/navdetour.cpp, without its
// wall margin: a headless client has no collision to catch on corners.
#include <cstdint>
#include <cstring>
#include <utility>

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
// The server's own query: one dtNavMeshQuery per map init'ed with SERVER_NODES nodes (the core's MoveMap.cpp:333).
const int SERVER_NODES = 2048;          // MoveMap.cpp:333
const int SERVER_MAX_POLYS = 256;       // PathFinder.h MAX_PATH_LENGTH
const int SERVER_MAX_POINTS = 256;      // PathFinder.h MAX_POINT_PATH_LENGTH
const float SMOOTH_PATH_SLOP = 0.4f;    // PathFinder.cpp getSteerTarget
const float SMOOTH_PATH_STEP_SIZE = 2.0f;  // PathFinder.cpp findSmoothPath
const unsigned PT_NORMAL = 0x1;         // PathFinder.h PATHFIND_NORMAL
const unsigned PT_SHORTCUT = 0x2;       // PATHFIND_SHORTCUT (unused: no swimming modelled)
const unsigned PT_INCOMPLETE = 0x4;     // PATHFIND_INCOMPLETE
const unsigned PT_NOPATH = 0x8;         // PATHFIND_NOPATH

struct MmapTileHeader {
    uint32_t mmapMagic, dtVersion, mmapVersion, size, usesLiquids;
};

struct Nav {
    dtNavMesh* mesh = nullptr;
    dtNavMeshQuery* query = nullptr;
    dtNavMeshQuery* server = nullptr;
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
    n->server = dtAllocNavMeshQuery();
    if (!n->mesh || !n->query || !n->server || dtStatusFailed(n->mesh->init(&p)) ||
        dtStatusFailed(n->query->init(n->mesh, MAX_NODES)) ||
        dtStatusFailed(n->server->init(n->mesh, SERVER_NODES))) {
        dtFreeNavMeshQuery(n->server);
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
    dtFreeNavMeshQuery(n->server);
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
    // Off-mesh links are traversable ground like the server's player filter
    // (include ground, exclude nothing) sees them: MoveMapGen writes their
    // flags 0xFF, whose magma/slime/steep bits would throw every link away
    // under the agent's exclude filter. Set them to NAV_GROUND (0x01).
    if (const dtMeshTile* tile = n->mesh->getTileByRef(ref)) {
        const dtPolyRef base = n->mesh->getPolyRefBase(tile);
        for (int i = 0; i < tile->header->polyCount; ++i) {
            if (tile->polys[i].getType() == DT_POLYTYPE_OFFMESH_CONNECTION)
                n->mesh->setPolyFlags(base | (dtPolyRef)i, 0x01);
        }
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
        const dtMeshTile* tile = 0;
        const dtPoly* poly = 0;
        if (dtStatusSucceed(n->mesh->getTileAndPolyByRef(refs[i], &tile, &poly)) &&
            poly->getType() == DT_POLYTYPE_OFFMESH_CONNECTION)
            continue; // a link is no surface: getPolyHeight would interpolate mid-air
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

namespace {
// clipPlane keeps the part of the convex polygon in (n points, WoW x, y, z) where coordinate axis is >= v (keepAbove) or <= v, written
// to out; it returns the new point count. The cut edges' points are interpolated on all three coordinates.
int clipPlane(const float* in, int n, int axis, float v, bool keepAbove, float* out) {
    int m = 0;
    for (int i = 0; i < n; ++i) {
        const float* a = &in[3 * i];
        const float* b = &in[3 * ((i + 1) % n)];
        const float da = keepAbove ? a[axis] - v : v - a[axis];
        const float db = keepAbove ? b[axis] - v : v - b[axis];
        if (da >= 0) {
            std::memcpy(&out[3 * m++], a, 3 * sizeof(float));
        }
        if ((da >= 0) != (db >= 0)) {
            const float t = da / (da - db);
            for (int k = 0; k < 3; ++k)
                out[3 * m + k] = a[k] + (b[k] - a[k]) * t;
            ++m;
        }
    }
    return m;
}

// area2D is the area of the polygon's (x, y) projection.
float area2D(const float* p, int n) {
    float a = 0;
    for (int i = 0; i < n; ++i) {
        const float* u = &p[3 * i];
        const float* w = &p[3 * ((i + 1) % n)];
        a += u[0] * w[1] - w[0] * u[1];
    }
    return a < 0 ? -a / 2 : a / 2;
}
}  // namespace

// The polygons whose part inside box = {x0, y0, z0, x1, y1, z1} (WoW, x0 <= x1, ...) covers at least share of their (x, y) area
// get flag added to their flags, in every loaded tile. The part inside is the polygon clipped by the box's six planes (its corners'
// heights interpolated along its edges), so a box that crosses a polygon without holding a corner counts, and a corner that dips
// into a box does not take a whole corridor polygon with it. Returns how many polygons it marked.
extern "C" int nd_mark_box(void* h, const float* box, unsigned short flag, float share) {
    Nav* n = (Nav*)h;
    if (!n || !n->mesh)
        return -1;
    dtNavMesh* mesh = n->mesh;
    int marked = 0;
    // A clip by six planes adds at most one point per plane.
    float bufA[3 * (DT_VERTS_PER_POLYGON + 8)], bufB[3 * (DT_VERTS_PER_POLYGON + 8)];
    for (int ti = 0; ti < mesh->getMaxTiles(); ++ti) {
        const dtMeshTile* t = ((const dtNavMesh*)mesh)->getTile(ti);
        if (!t || !t->header)
            continue;
        if (t->header->bmax[2] < box[0] || t->header->bmin[2] > box[3] || t->header->bmax[0] < box[1] ||
            t->header->bmin[0] > box[4])
            continue;
        const dtPolyRef base = mesh->getPolyRefBase(t);
        for (int pi = 0; pi < t->header->polyCount; ++pi) {
            const dtPoly* p = &t->polys[pi];
            // Off-mesh links are never marked: an avoid box over a link's
            // middle must not cut the route that crosses it.
            if (p->getType() == DT_POLYTYPE_OFFMESH_CONNECTION)
                continue;
            if (p->getType() != DT_POLYTYPE_GROUND || p->vertCount < 3)
                continue;
            int m = p->vertCount;
            for (int k = 0; k < m; ++k)
                toWow(&t->verts[p->verts[k] * 3], &bufA[3 * k]);
            const float whole = area2D(bufA, m);
            if (whole <= 0)
                continue;
            float* cur = bufA;
            float* nxt = bufB;
            for (int axis = 0; axis < 3 && m > 0; ++axis) {
                m = clipPlane(cur, m, axis, box[axis], true, nxt);
                std::swap(cur, nxt);
                if (m == 0)
                    break;
                m = clipPlane(cur, m, axis, box[axis + 3], false, nxt);
                std::swap(cur, nxt);
            }
            if (m < 3 || area2D(cur, m) < share * whole)
                continue;
            if (dtStatusSucceed(mesh->setPolyFlags(base | (dtPolyRef)pi, p->flags | flag)))
                ++marked;
        }
    }
    return marked;
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
                            unsigned short exclude, float* out, unsigned char* flags, int max_points,
                            int* count, float* goal) {
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
    unsigned char sflags[2048];
    dtPolyRef refs[2048];
#ifdef ND_TEST_POISON_SFLAGS
    // Test builds fill the scratch flags first, so a rebuild that reads them
    // back without findStraightPath having written them fails deterministically
    // instead of depending on whatever the stack held.
    memset(sflags, 0xFF, sizeof(sflags));
#endif
    // findStraightPath always writes here; the rebuild below reads this array
    // back, and the caller's flags get the amended copy afterwards.
    memset(sflags, 0, sizeof(sflags));
    if (dtStatusFailed(n->query->findStraightPath(sc, ec, polys, np, out, sflags, refs, &npts,
                                                  max_points)) ||
        npts < 1)
        return -3;
    // An off-mesh link is recovered from the poly corridor: the straight
    // corner at the link's far end carries DT_STRAIGHTPATH_OFFMESH_CONNECTION
    // (meaning the segment from that corner is the link). The funnel may merge
    // the link's two ends into one corner or skip the flag, so mark or split
    // here: for each link poly, find its far end and either flag the corner
    // nearest to it or split the crossed segment there.
    if (npts < max_points) {
        float out2[3 * 2048];
        unsigned char fl2[2048];
        for (int i = 0; i < npts; ++i) {
            dtVcopy(&out2[i * 3], &out[i * 3]);
            fl2[i] = sflags[i];
        }
        int w = npts;
        for (int k = 1; k + 1 < np; ++k) {
            const dtMeshTile* tile = 0;
            const dtPoly* poly = 0;
            if (!dtStatusSucceed(n->mesh->getTileAndPolyByRef(polys[k], &tile, &poly)) ||
                poly->getType() != DT_POLYTYPE_OFFMESH_CONNECTION)
                continue;
            float s2[3], e2[3];
            if (!dtStatusSucceed(n->mesh->getOffMeshConnectionPolyEndPoints(polys[k - 1], polys[k], s2, e2)))
                continue;
            if (!dtStatusSucceed(n->mesh->getOffMeshConnectionPolyEndPoints(polys[k - 1], polys[k], s2, e2)))
                continue;
            // No corner at the far end: split the segment whose span passes
            // nearest to it.
            int bi = -1;
            float bd = 1e30f;
            for (int i = 0; i + 1 < w && w < 2048 && w < max_points; ++i) {
                float ab[3], ap[3];
                dtVsub(ab, &out2[(i + 1) * 3], &out2[i * 3]);
                dtVsub(ap, e2, &out2[i * 3]);
                float denom = dtVdot(ab, ab);
                float t = denom > 0 ? dtVdot(ap, ab) / denom : 0;
                t = t < 0 ? 0 : (t > 1 ? 1 : t);
                float q[3], dd[3];
                dtVmad(q, &out2[i * 3], ab, t);
                dtVsub(dd, e2, q);
                float d2 = dtVlenSqr(dd);
                if (d2 < bd) {
                    bd = d2;
                    bi = i;
                }
            }
            if (bi < 0 || bd > 0.25f)
                continue;
            for (int i = w; i > bi + 1; --i) {
                dtVcopy(&out2[i * 3], &out2[(i - 1) * 3]);
                fl2[i] = fl2[i - 1];
            }
            fl2[bi] = DT_STRAIGHTPATH_OFFMESH_CONNECTION;
            dtVcopy(&out2[(bi + 1) * 3], e2);
            fl2[bi + 1] = 0;
            ++w;
        }
        for (int i = 0; i < w; ++i) {
            dtVcopy(&out[i * 3], &out2[i * 3]);
            sflags[i] = fl2[i];
        }
        npts = w;
    }
    if (flags) {
        for (int i = 0; i < npts; ++i)
            flags[i] = sflags[i];
    }
    for (int i = 0; i < npts; ++i) {
        float d[3];
        dtVcopy(d, &out[i * 3]);
        toWow(d, &out[i * 3]);
    }
    *count = npts;
    return result;
}
// ---- The server's own path query: PathInfo::BuildPolyPath + findSmoothPath (core
// PathFinder.cpp:597-916), copied from specs/plancheck-vs-server-path/repro.cpp, which
// is itself line-for-line from the core. ----

// True when a lies within r (horizontally) and h (vertically) of b, in Detour (y, z, x) order.
static bool inRangeYZX(const float* a, const float* b, float r, float h) {
    float dx = b[0] - a[0], dy = b[1] - a[1], dz = b[2] - a[2];
    return dx * dx + dz * dz < r * r && fabsf(dy) < h;
}

// PathFinder.cpp fixupCorridor.
static uint32_t fixupCorridor(dtPolyRef* path, uint32_t npath, uint32_t maxPath, const dtPolyRef* visited,
                              uint32_t nvisited) {
    int fp = -1, fv = -1;
    for (int i = npath - 1; i >= 0; --i) {
        bool found = false;
        for (int j = nvisited - 1; j >= 0; --j) {
            if (path[i] == visited[j]) {
                fp = i;
                fv = j;
                found = true;
            }
        }
        if (found)
            break;
    }
    if (fp == -1 || fv == -1)
        return npath;
    uint32_t req = nvisited - fv, orig = uint32_t(fp + 1) < npath ? fp + 1 : npath,
             size = npath > orig ? npath - orig : 0;
    // Keep the copy inside the buffer: req + size never exceeds maxPath, and the
    // subtraction cannot underflow (the bound -Wstringop-overflow asked for; the
    // query passes maxPath = SERVER_MAX_POLYS for a polys buffer of that size).
    if (req > maxPath)
        req = maxPath;
    size = dtMin(size, maxPath - req);
    if (size)
        memmove(path + req, path + orig, size * sizeof(dtPolyRef));
    for (uint32_t i = 0; i < req; ++i)
        path[i] = visited[(nvisited - 1) - i];
    return req + size;
}

// PathFinder.cpp fixupShortcuts.
static int fixupShortcuts(dtPolyRef* path, int npath, dtNavMeshQuery* q) {
    if (npath < 3)
        return npath;
    dtPolyRef neis[16];
    int nn = 0;
    const dtMeshTile* t = 0;
    const dtPoly* p = 0;
    if (dtStatusFailed(q->getAttachedNavMesh()->getTileAndPolyByRef(path[0], &t, &p)))
        return npath;
    for (unsigned k = p->firstLink; k != DT_NULL_LINK; k = t->links[k].next) {
        if (t->links[k].ref && nn < 16)
            neis[nn++] = t->links[k].ref;
    }
    int cut = 0;
    for (int i = dtMin(6, npath) - 1; i > 1 && cut == 0; i--) {
        for (int j = 0; j < nn; j++) {
            if (path[i] == neis[j]) {
                cut = i;
                break;
            }
        }
    }
    if (cut > 1) {
        int off = cut - 1;
        npath -= off;
        for (int i = 1; i < npath; i++)
            path[i] = path[i + off];
    }
    return npath;
}

// PathFinder.cpp getSteerTarget (named steer in the spec's repro.cpp).
static bool steer(dtNavMeshQuery* q, const float* sp, const float* ep, const dtPolyRef* path, uint32_t n,
                  float* pos, unsigned char& fl, dtPolyRef& ref) {
    float sp3[9];
    unsigned char f3[3];
    dtPolyRef r3[3];
    int ns = 0;
    if (dtStatusFailed(q->findStraightPath(sp, ep, path, n, sp3, f3, r3, &ns, 3)) || !ns)
        return false;
    int i = 0;
    while (i < ns) {
        if ((f3[i] & DT_STRAIGHTPATH_OFFMESH_CONNECTION) || !inRangeYZX(&sp3[i * 3], sp, SMOOTH_PATH_SLOP, 1000.0f))
            break;
        i++;
    }
    if (i >= ns)
        return false;
    dtVcopy(pos, &sp3[i * 3]);
    pos[1] = sp[1];
    fl = f3[i];
    ref = r3[i];
    return true;
}

// PathFinder.cpp findSmoothPath (named smooth in the spec's repro.cpp).
static dtStatus smooth(dtNavMeshQuery* q, const dtQueryFilter& f, const float* sp, const float* ep,
                       const dtPolyRef* pp, uint32_t pn, float* out, int* on, uint32_t maxS,
                       uint32_t maxPoly) {
    *on = 0;
    uint32_t ns = 0;
    dtPolyRef polys[SERVER_MAX_POLYS];
    for (uint32_t i = 0; i < pn && i < SERVER_MAX_POLYS; ++i)
        polys[i] = pp[i];
    uint32_t np = pn;
    float it[3], tg[3];
    if (dtStatusFailed(q->closestPointOnPolyBoundary(polys[0], sp, it)))
        return DT_FAILURE;
    if (dtStatusFailed(q->closestPointOnPolyBoundary(polys[np - 1], ep, tg)))
        return DT_FAILURE;
    dtVcopy(&out[ns * 3], it);
    ns++;
    while (np && ns < maxS) {
        float st[3];
        unsigned char sf;
        dtPolyRef sr = 0;
        if (!steer(q, it, tg, polys, np, st, sf, sr))
            break;
        bool eop = sf & DT_STRAIGHTPATH_END, om = sf & DT_STRAIGHTPATH_OFFMESH_CONNECTION;
        float d[3];
        dtVsub(d, st, it);
        float len = sqrtf(dtVdot(d, d));
        float step = SMOOTH_PATH_STEP_SIZE;
        if ((eop || om) && len < step)
            len = 1.0f;
        else if (len < step * 4)
            len = step / len;
        else
            len = step * 4 / len;
        float mt[3];
        dtVmad(mt, it, d, len);
        float res[3];
        dtPolyRef vis[16];
        int nv = 0;
        if (dtStatusFailed(q->moveAlongSurface(polys[0], it, mt, &f, res, vis, &nv, 16)))
            return DT_FAILURE;
        np = fixupCorridor(polys, np, maxPoly, vis, nv);
        np = fixupShortcuts(polys, np, q);
        q->getPolyHeight(polys[0], res, &res[1]);
        res[1] += 0.5f;
        dtVcopy(it, res);
        if (eop && inRangeYZX(it, st, SMOOTH_PATH_SLOP, 1.0f)) {
            if (dtStatusSucceed(q->getPolyHeight(polys[0], tg, &tg[1]))) {
                tg[1] += 0.5f;
                dtVcopy(it, tg);
            }
            if (ns < maxS) {
                dtVcopy(&out[ns * 3], it);
                ns++;
            }
            break;
        } else if (om && inRangeYZX(it, st, SMOOTH_PATH_SLOP, 1.0f)) {
            dtPolyRef prev = 0, pr = polys[0];
            uint32_t npos = 0;
            while (npos < np && pr != sr) {
                prev = pr;
                pr = polys[npos];
                npos++;
            }
            for (uint32_t i = npos; i < np; ++i)
                polys[i - npos] = polys[i];
            np -= npos;
            float s2[3], e2[3];
            if (dtStatusSucceed(q->getAttachedNavMesh()->getOffMeshConnectionPolyEndPoints(prev, pr, s2, e2))) {
                if (ns < maxS) {
                    dtVcopy(&out[ns * 3], s2);
                    ns++;
                }
                dtVcopy(it, e2);
                if (dtStatusFailed(q->getPolyHeight(polys[0], it, &it[1])))
                    return DT_FAILURE;
                it[1] += 0.2f;
            }
        }
        if (ns < maxS) {
            dtVcopy(&out[ns * 3], it);
            ns++;
        }
    }
    *on = ns;
    return ns < 256 ? DT_SUCCESS : DT_FAILURE;
}

// PathInfo::FindWalkPoly on the server query (extents 5/10/5 and the +3 rule, PathFinder.cpp:160-175).
static dtPolyRef serverWalkPoly(Nav* n, const float* p, const dtQueryFilter& f, float* closest, float* dist) {
    const float ext[3] = {5.0f, 10.0f, 5.0f};
    dtPolyRef ref = 0;
    if (dtStatusFailed(n->server->findNearestPoly(p, ext, &f, &ref, closest)) || !ref)
        return 0;
    if (closest[1] > p[1] + 3.0f)
        return 0;
    *dist = dtVdist(closest, p);
    return ref;
}

// nd_server_path: the server's exact query and verdict inputs (PathInfo::BuildPolyPath +
// findSmoothPath, PathFinder.cpp:240-265, :384-392, :405-452, :915), with the server's
// filter (include ground|water, exclude 0; PathFinder.cpp:547-566). start and end are WoW
// (x, y, z); out takes up to max_points points in the same order; actual_end starts as end
// and ends as the last smooth point (what the run leader walks to); type is one of
// PT_NORMAL/PT_INCOMPLETE/PT_NOPATH; start_dist/end_dist are the asked point's distance
// to its walk poly (0 when there is none). 0 done, -1 for a null handle.
extern "C" int nd_server_path(void* h, const float* start, const float* end, float* out, int max_points,
                              int* count, float* actual_end, unsigned* type, float* start_dist,
                              float* end_dist) {
    Nav* n = (Nav*)h;
    if (!n || !n->server)
        return -1;
    *count = 0;
    *type = PT_NOPATH;
    *start_dist = 0;
    *end_dist = 0;
    dtQueryFilter f = makeFilter(0x09, 0);
    float s[3], e[3], sc[3], ec[3], sdist = 0, edist = 0;
    toDt(start, s);
    toDt(end, e);
    toWow(e, actual_end);
    dtPolyRef sref = serverWalkPoly(n, s, f, sc, &sdist);
    dtPolyRef eref = serverWalkPoly(n, e, f, ec, &edist);
    *start_dist = sdist;
    *end_dist = edist;
    if (!sref || !eref) {
        *type = PT_NOPATH;
        return 0;
    }
    // BuildPolyPath's rule stays: a goal over 7 yd from the mesh clamps the end to the
    // poly boundary and starts incomplete (PathFinder.cpp:240-265; swimming is not modelled).
    unsigned incomplete = 0;
    if (sdist > 7.0f || edist > 7.0f) {
        float cp[3];
        if (dtStatusSucceed(n->server->closestPointOnPolyBoundary(eref, e, cp))) {
            dtVcopy(e, cp);
            toWow(cp, actual_end);
        }
        incomplete = PT_INCOMPLETE;
    }
    // The raw asked start and end, not the floor points (PathFinder.cpp:384-392).
    dtPolyRef polys[SERVER_MAX_POLYS];
    int np = 0;
    dtStatus st = n->server->findPath(sref, eref, s, e, &f, polys, &np, SERVER_MAX_POLYS);
    if (!np || dtStatusFailed(st)) {
        *type = PT_NOPATH;
        return 0;
    }
    *type = (polys[np - 1] == eref && !incomplete) ? PT_NORMAL : PT_INCOMPLETE;
    float pts[SERVER_MAX_POINTS * 3];
    int nsmooth = 0;
    dtStatus r = smooth(n->server, f, s, e, polys, np, pts, &nsmooth, SERVER_MAX_POINTS, SERVER_MAX_POLYS);
    if (nsmooth < 2 || dtStatusFailed(r)) {
        *type = PT_NOPATH;
        *count = 0;
        return 0;
    }
    int nout = nsmooth < max_points ? nsmooth : max_points;
    for (int i = 0; i < nout; ++i) {
        float d[3];
        dtVcopy(d, &pts[i * 3]);
        toWow(d, &out[i * 3]);
    }
    *count = nout;
    toWow(&pts[(nsmooth - 1) * 3], actual_end);
    return 0;
}
