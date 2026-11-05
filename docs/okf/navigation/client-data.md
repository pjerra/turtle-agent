---
type: Concept
title: Client data: MPQ and BLP
description: The game client's archives and textures the minimap pictures come from.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/mpq/mpq.go
tags: [navigation, minimap, client]
sources:
  - id: mpq
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/mpq/mpq.go
    title: The MPQ archive reader
  - id: blp
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/blp/blp.go
    title: The BLP texture decoder
  - id: minimap-tiles
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/minimap/tiles.go
    title: Minimap tiles and coordinates
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# Client data: MPQ and BLP

The dashboard's background pictures come from the game client you own. Two small readers pull them out of the client's files: MPQ opens the archives, BLP decodes the textures.

## MPQ archives

MPQ is Blizzard's archive format. The client's `Data` folder holds a stack of them (`dbc`, `terrain`, `wmo`, `base`, patches `patch-2` through `patch-9`); later archives win, so a patched file comes from the newest archive that holds it. The reader is read-only and pure Go: it checks the `MPQ\x1a` header, decrypts the hash and block tables, and decompresses each file's sectors (stored, zlib, bzip2 or PKWARE-implode; audio-only compressions are refused). Files it cannot support fail loudly: binary patch files are not supported, and a folder with none of the archives fails with `mpq: none of the archives exist in <dir>`.

## BLP textures

BLP is the client's texture format. The decoder reads BLP2 files (paletted, DXT1/3/5 or raw BGRA) into images, first and largest mip only; JPEG-compressed BLP is not supported. Each minimap tile is one BLP: outdoor terrain tiles named by md5 hash, indoor WMO tiles per placed model group.

## How the pictures are built

`minimap-extract` opens the archive set, reads `Map.dbc` (which maps are dungeons and raids), `md5translate.trs` (which hash each terrain tile has) and `WorldMapArea.dbc` (the area picture edges), then stitches the tiles: terrain at 256 pixels per 533.33-yd tile, indoor WMO tiles at 2 pixels per yard over or instead of the terrain, the area picture cut from 4x3 tiles. A map with no client tiles gets a height-shaded render of its navmesh instead (needs `--mmaps`). Nothing is ever written back: the client folder is read only.
