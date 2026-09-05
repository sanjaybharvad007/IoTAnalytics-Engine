package geofence

import "testing"

func TestContains(t *testing.T) {
	// A 1km-radius zone centered on downtown San Jose.
	z := Zone{Name: "depot", Center: Point{37.3382, -121.8863}, RadiusM: 1000}
	if !z.Contains(Point{37.3385, -121.8865}) {
		t.Error("nearby point should be inside")
	}
	if z.Contains(Point{37.40, -121.95}) {
		t.Error("far point should be outside")
	}
}

func TestEnterExitTransitions(t *testing.T) {
	z := Zone{Name: "depot", Center: Point{37.3382, -121.8863}, RadiusM: 1000}
	m := NewMonitor(z)

	// First reading outside -> no transition.
	if len(m.Update("van-1", Point{37.50, -121.99})) != 0 {
		t.Error("no transition expected on first outside reading")
	}
	// Move inside -> enter.
	tr := m.Update("van-1", Point{37.3383, -121.8864})
	if len(tr) != 1 || tr[0].Event != "enter" {
		t.Errorf("expected enter, got %+v", tr)
	}
	// Stay inside -> no new transition.
	if len(m.Update("van-1", Point{37.3384, -121.8865})) != 0 {
		t.Error("no transition while staying inside")
	}
	// Move outside -> exit.
	tr = m.Update("van-1", Point{37.50, -121.99})
	if len(tr) != 1 || tr[0].Event != "exit" {
		t.Errorf("expected exit, got %+v", tr)
	}
}
