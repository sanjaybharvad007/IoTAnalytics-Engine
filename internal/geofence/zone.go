// Package geofence raises alerts when a vehicle enters or leaves a defined
// geographic area. It supports circular zones (great-circle radius) and
// arbitrary polygons (ray casting), and a Monitor that tracks each vehicle's
// inside/outside state so events fire only on boundary crossings.
package geofence

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"iotanalytics/internal/geo"
)

// Zone is any area that can decide whether a point lies within it.
type Zone interface {
	Name() string
	Contains(p geo.Point) bool
	// Spec returns a serialisable description used by APIs and dashboards.
	Spec() Spec
}

// Spec is the wire/JSON representation of a zone.
type Spec struct {
	Name     string      `json:"name"`
	Type     string      `json:"type"` // "circle" | "polygon"
	Center   *geo.Point  `json:"center,omitempty"`
	RadiusM  float64     `json:"radius_m,omitempty"`
	Vertices []geo.Point `json:"vertices,omitempty"`
}

// Circle is a zone defined by a centre point and a radius in metres.
type Circle struct {
	ZoneName string
	Center   geo.Point
	RadiusM  float64
}

// Name implements Zone.
func (c Circle) Name() string { return c.ZoneName }

// Contains reports whether p lies within the great-circle radius.
func (c Circle) Contains(p geo.Point) bool { return geo.Distance(c.Center, p) <= c.RadiusM }

// Spec implements Zone.
func (c Circle) Spec() Spec {
	center := c.Center
	return Spec{Name: c.ZoneName, Type: "circle", Center: &center, RadiusM: c.RadiusM}
}

// Polygon is a zone bounded by an ordered ring of vertices. The ring is
// implicitly closed; it need not repeat the first vertex.
type Polygon struct {
	ZoneName string
	Vertices []geo.Point
}

// Name implements Zone.
func (pg Polygon) Name() string { return pg.ZoneName }

// Contains uses the even-odd ray-casting test. For zones of city scale,
// treating lat/lng as planar coordinates is accurate to well under a metre.
func (pg Polygon) Contains(p geo.Point) bool {
	n := len(pg.Vertices)
	if n < 3 {
		return false
	}
	inside := false
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		vi, vj := pg.Vertices[i], pg.Vertices[j]
		if (vi.Lat > p.Lat) != (vj.Lat > p.Lat) {
			x := (vj.Lng-vi.Lng)*(p.Lat-vi.Lat)/(vj.Lat-vi.Lat) + vi.Lng
			if p.Lng < x {
				inside = !inside
			}
		}
	}
	return inside
}

// Spec implements Zone.
func (pg Polygon) Spec() Spec {
	return Spec{Name: pg.ZoneName, Type: "polygon", Vertices: append([]geo.Point(nil), pg.Vertices...)}
}

// ErrInvalidZone is wrapped by FromSpec for malformed zone definitions.
var ErrInvalidZone = errors.New("geofence: invalid zone")

// FromSpec builds a Zone from its serialised description.
func FromSpec(s Spec) (Zone, error) {
	if s.Name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalidZone)
	}
	switch s.Type {
	case "circle":
		if s.Center == nil || s.RadiusM <= 0 {
			return nil, fmt.Errorf("%w %q: circle needs center and radius_m > 0", ErrInvalidZone, s.Name)
		}
		if err := s.Center.Validate(); err != nil {
			return nil, fmt.Errorf("%w %q: %v", ErrInvalidZone, s.Name, err)
		}
		return Circle{ZoneName: s.Name, Center: *s.Center, RadiusM: s.RadiusM}, nil
	case "polygon":
		if len(s.Vertices) < 3 {
			return nil, fmt.Errorf("%w %q: polygon needs at least 3 vertices", ErrInvalidZone, s.Name)
		}
		for _, v := range s.Vertices {
			if err := v.Validate(); err != nil {
				return nil, fmt.Errorf("%w %q: %v", ErrInvalidZone, s.Name, err)
			}
		}
		return Polygon{ZoneName: s.Name, Vertices: append([]geo.Point(nil), s.Vertices...)}, nil
	default:
		return nil, fmt.Errorf("%w %q: unknown type %q", ErrInvalidZone, s.Name, s.Type)
	}
}

// Parse decodes a JSON array of zone specs.
func Parse(data []byte) ([]Zone, error) {
	var specs []Spec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, fmt.Errorf("geofence: decode zones: %w", err)
	}
	zones := make([]Zone, 0, len(specs))
	seen := make(map[string]bool, len(specs))
	for _, s := range specs {
		if seen[s.Name] {
			return nil, fmt.Errorf("%w: duplicate zone name %q", ErrInvalidZone, s.Name)
		}
		seen[s.Name] = true
		z, err := FromSpec(s)
		if err != nil {
			return nil, err
		}
		zones = append(zones, z)
	}
	return zones, nil
}

// LoadFile reads zone definitions from a JSON file.
func LoadFile(path string) ([]Zone, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("geofence: read %s: %w", path, err)
	}
	return Parse(data)
}
