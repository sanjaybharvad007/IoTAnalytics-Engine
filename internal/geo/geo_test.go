package geo

import (
	"math"
	"testing"
)

var (
	sanJose = Point{Lat: 37.3382, Lng: -121.8863}
	sanFran = Point{Lat: 37.7749, Lng: -122.4194}
)

func TestDistanceKnownValue(t *testing.T) {
	// San Jose -> San Francisco is ~68 km great-circle.
	got := Distance(sanJose, sanFran)
	if math.Abs(got-68_000) > 1_500 {
		t.Fatalf("distance = %.0f m, want ~68 km", got)
	}
	if Distance(sanJose, sanJose) != 0 {
		t.Fatal("distance to self must be 0")
	}
	if d1, d2 := Distance(sanJose, sanFran), Distance(sanFran, sanJose); d1 != d2 {
		t.Fatalf("distance must be symmetric: %v vs %v", d1, d2)
	}
}

func TestOffsetRoundTrip(t *testing.T) {
	for _, bearing := range []float64{0, 45, 90, 180, 270, 359} {
		q := Offset(sanJose, bearing, 2_500)
		if d := Distance(sanJose, q); math.Abs(d-2_500) > 0.5 {
			t.Errorf("bearing %v: offset distance %.2f, want 2500", bearing, d)
		}
		if b := Bearing(sanJose, q); math.Abs(b-bearing) > 0.1 {
			t.Errorf("bearing %v: got %.2f", bearing, b)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		p    Point
		want error
	}{
		{Point{0, 0}, nil},
		{Point{90, 180}, nil},
		{Point{-90.001, 0}, ErrOutOfRange},
		{Point{0, 180.5}, ErrOutOfRange},
		{Point{math.NaN(), 0}, ErrOutOfRange},
	}
	for _, c := range cases {
		if got := c.p.Validate(); got != c.want {
			t.Errorf("Validate(%v) = %v, want %v", c.p, got, c.want)
		}
	}
}

func BenchmarkDistance(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Distance(sanJose, sanFran)
	}
}
