// Package minimap extracts the in-game minimap of a map from the client's MPQ archives and describes where its pixels lie
// in world coordinates, so that the dashboard can draw a run over the real map.
package minimap

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// DBC is a WDBC table of the client: fixed-size records of 32-bit fields and a string block.
type DBC struct {
	Records, Fields, RecordSize int
	data                        []byte
	strings                     []byte
}

// ParseDBC checks the WDBC header and slices out the records and the strings.
func ParseDBC(b []byte) (*DBC, error) {
	if len(b) < 20 || string(b[:4]) != "WDBC" {
		return nil, errors.New("dbc: not a WDBC file")
	}
	le := binary.LittleEndian
	d := &DBC{Records: int(le.Uint32(b[4:])), Fields: int(le.Uint32(b[8:])), RecordSize: int(le.Uint32(b[12:]))}
	ss := int(le.Uint32(b[16:]))
	if d.Records < 0 || d.RecordSize < 4 || d.Fields < 1 || d.RecordSize != d.Fields*4 || 20+d.Records*d.RecordSize+ss > len(b) {
		return nil, fmt.Errorf("dbc: bad header (%d records of %d bytes, %d string bytes, file %d)", d.Records, d.RecordSize, ss, len(b))
	}
	d.data = b[20 : 20+d.Records*d.RecordSize]
	d.strings = b[20+d.Records*d.RecordSize : 20+d.Records*d.RecordSize+ss]
	return d, nil
}

// U32 is field f of record r.
func (d *DBC) U32(r, f int) uint32 {
	return binary.LittleEndian.Uint32(d.data[r*d.RecordSize+f*4:])
}

// Str is the string field f of record r ("" for an offset outside the block).
func (d *DBC) Str(r, f int) string {
	off := int(d.U32(r, f))
	if off <= 0 || off >= len(d.strings) {
		return ""
	}
	end := off
	for end < len(d.strings) && d.strings[end] != 0 {
		end++
	}
	return string(d.strings[off:end])
}

// Instance types of Map.dbc.
const (
	InstanceNone   = 0
	InstanceParty  = 1
	InstanceRaid   = 2
	InstanceBG     = 3
	InstanceArena  = 4
	mapDBCPath     = `DBFilesClient\Map.dbc`
	mapFieldDir    = 1
	mapFieldType   = 2
	mapFieldName   = 4 // MapName_lang, enUS
	mapMinFields   = 6
	mapDBCFallback = "Map.dbc"
)

// MapRow is one row of Map.dbc.
type MapRow struct {
	ID           uint32
	Dir          string // the folder name under World\Maps and Textures\Minimap
	InstanceType uint32
	Name         string
}

// Instance reports whether the map is a dungeon (party) or a raid.
func (m MapRow) Instance() bool {
	return m.InstanceType == InstanceParty || m.InstanceType == InstanceRaid
}

// ParseMaps reads Map.dbc.
func ParseMaps(b []byte) ([]MapRow, error) {
	d, err := ParseDBC(b)
	if err != nil {
		return nil, err
	}
	if d.Fields < mapMinFields {
		return nil, fmt.Errorf("the Map.dbc has %d fields; expected a 1.12 layout", d.Fields)
	}
	rows := make([]MapRow, d.Records)
	for i := range rows {
		rows[i] = MapRow{ID: d.U32(i, 0), Dir: d.Str(i, mapFieldDir), InstanceType: d.U32(i, mapFieldType), Name: d.Str(i, mapFieldName)}
	}
	return rows, nil
}

// WorldMapArea is a row of WorldMapArea.dbc: the world-map picture of an area (a zone, or an instance) and the world
// coordinates of its edges.
type WorldMapArea struct {
	Map                      uint32
	Name                     string  // folder under Interface\WorldMap, "" for a continent row
	Left, Right, Top, Bottom float64 // world y of the left and right edges, world x of the top and bottom edges
}

// ParseWorldMapAreas reads WorldMapArea.dbc.
func ParseWorldMapAreas(b []byte) ([]WorldMapArea, error) {
	d, err := ParseDBC(b)
	if err != nil {
		return nil, err
	}
	if d.Fields < 8 {
		return nil, fmt.Errorf("WorldMapArea.dbc has %d fields; expected 8", d.Fields)
	}
	f := func(r, i int) float64 { return float64(math.Float32frombits(d.U32(r, i))) }
	rows := make([]WorldMapArea, d.Records)
	for i := range rows {
		rows[i] = WorldMapArea{Map: d.U32(i, 1), Name: d.Str(i, 3), Left: f(i, 4), Right: f(i, 5), Top: f(i, 6), Bottom: f(i, 7)}
	}
	return rows, nil
}
