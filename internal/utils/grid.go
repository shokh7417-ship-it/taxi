package utils

import (
	"fmt"
	"math"
)

// GridSizeDeg is the grid cell size in degrees (~0.005 ≈ 500m).
const GridSizeDeg = 0.005

// GridCell returns integer grid coordinates for the given lat/lng.
func GridCell(lat, lng float64) (int64, int64) {
	x := int64(math.Floor(lat / GridSizeDeg))
	y := int64(math.Floor(lng / GridSizeDeg))
	return x, y
}

// GridID returns a string identifier for the cell: "x:y".
func GridID(lat, lng float64) string {
	x, y := GridCell(lat, lng)
	return fmt.Sprintf("%d:%d", x, y)
}

// NeighborGridIDs returns the current cell and its 8 neighbors for the given lat/lng.
func NeighborGridIDs(lat, lng float64) []string {
	x, y := GridCell(lat, lng)
	out := make([]string, 0, 9)
	for dx := int64(-1); dx <= 1; dx++ {
		for dy := int64(-1); dy <= 1; dy++ {
			out = append(out, fmt.Sprintf("%d:%d", x+dx, y+dy))
		}
	}
	return out
}

