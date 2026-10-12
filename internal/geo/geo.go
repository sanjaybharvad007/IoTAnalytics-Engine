// Package geo holds the small amount of geodesy the engine needs: GPS points,
// great-circle distance and bearing. It is dependency-free and shared by the
// aggregation (distance travelled) and geofence (zone membership) packages.
package geo

import (
	"errors"
	"math"
)

// EarthRadiusM is the mean Earth radius in metres (IUGG value).
const EarthRadiusM = 6371008.8

// Point is a WGS-84 coordinate in decimal degrees.
type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// ErrOutOfRange is returned by Validate for coordinates outside the WGS-84 domain.
var ErrOutOfRange = errors.New("geo: coordinate out of range")

// Validate reports whether the point is a legal latitude/longitude pair.
func (p Point) Validate() error {
	if math.IsNaN(p.Lat) || math.IsNaN(p.Lng) ||
		p.Lat < -90 || p.Lat > 90 || p.Lng < -180 || p.Lng > 180 {
		return ErrOutOfRange
	}
	return nil
}

// IsZero reports whether the point is the (0,0) null island sentinel, which the
// engine treats as "no fix".
func (p Point) IsZero() bool { return p.Lat == 0 && p.Lng == 0 }

func rad(deg float64) float64 { return deg * math.Pi / 180 }
func deg(rad float64) float64 { return rad * 180 / math.Pi }

// Distance returns the great-circle distance between two points in metres
// using the haversine formula, which is numerically stable for the short
// distances typical of fleet telemetry.
func Distance(a, b Point) float64 {
	lat1, lat2 := rad(a.Lat), rad(b.Lat)
	dLat := rad(b.Lat - a.Lat)
	dLng := rad(b.Lng - a.Lng)

	sinLat := math.Sin(dLat / 2)
	sinLng := math.Sin(dLng / 2)
	h := sinLat*sinLat + math.Cos(lat1)*math.Cos(lat2)*sinLng*sinLng
	return 2 * EarthRadiusM * math.Asin(math.Min(1, math.Sqrt(h)))
}

// Bearing returns the initial compass bearing from a to b in degrees [0, 360).
func Bearing(a, b Point) float64 {
	lat1, lat2 := rad(a.Lat), rad(b.Lat)
	dLng := rad(b.Lng - a.Lng)
	y := math.Sin(dLng) * math.Cos(lat2)
	x := math.Cos(lat1)*math.Sin(lat2) - math.Sin(lat1)*math.Cos(lat2)*math.Cos(dLng)
	return math.Mod(deg(math.Atan2(y, x))+360, 360)
}

// Offset returns the point reached by travelling distM metres from p along
// the given bearing (degrees). Useful for simulators and tests.
func Offset(p Point, bearingDeg, distM float64) Point {
	ad := distM / EarthRadiusM
	br := rad(bearingDeg)
	lat1, lng1 := rad(p.Lat), rad(p.Lng)

	lat2 := math.Asin(math.Sin(lat1)*math.Cos(ad) + math.Cos(lat1)*math.Sin(ad)*math.Cos(br))
	lng2 := lng1 + math.Atan2(math.Sin(br)*math.Sin(ad)*math.Cos(lat1),
		math.Cos(ad)-math.Sin(lat1)*math.Sin(lat2))
	return Point{Lat: deg(lat2), Lng: math.Mod(deg(lng2)+540, 360) - 180}
}
