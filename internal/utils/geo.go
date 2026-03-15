package utils

import "math"

// HaversineMeters returns the great-circle distance in meters between two
// points on Earth given their latitude and longitude in degrees.
func HaversineMeters(lat1, lng1, lat2, lng2 float64) float64 {
	const earthRadiusM = 6_371_000 // meters
	dLat := rad(lat2 - lat1)
	dLng := rad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusM * c
}

func rad(deg float64) float64 {
	return deg * math.Pi / 180
}
