// Command server demos the full analytics engine: telematics through the
// sliding-window aggregator, the safety-alert engine, and geofencing — with
// durable storage and a live broker. Run: go run ./cmd/server
package main

import (
	"log"
	"math/rand"
	"time"

	"iotanalytics/aggregation-go"
	"iotanalytics/alerts-go"
	"iotanalytics/geofence-go"
	"iotanalytics/storage-go"
	"iotanalytics/telematics-go"
)

func main() {
	window := aggregation.NewWindow(time.Minute)
	engine := alerts.NewEngine(alerts.SpeedingRule(100), alerts.LowFuelRule(15))

	// A depot geofence in San Jose; alert when a vehicle leaves it.
	depot := geofence.Zone{Name: "depot", Center: geofence.Point{Lat: 37.3382, Lng: -121.8863}, RadiusM: 1000}
	monitor := geofence.NewMonitor(depot)

	store, err := storage.Open("ops.log")
	if err != nil { log.Fatal(err) }
	defer store.Close()
	broker := storage.NewBroker()

	now := time.Now()
	for i := 0; i < 200; i++ {
		r := telematics.Reading{
			VehicleID: "van-7", Speed: 60 + rand.NormFloat64()*15,
			FuelLevel: 100 - float64(i)*0.5, EngineOn: true,
			Timestamp: now.Add(time.Duration(i) * time.Second),
		}
		if i == 150 { r.Speed = 140 }
		window.Add(r)

		for _, a := range engine.Process(r) {
			store.Append(storage.Record{Type: "alert", Vehicle: a.VehicleID, Payload: a.Message})
			broker.Publish("ALERT " + a.Kind + ": " + a.Message)
		}

		// Vehicle drives out of the depot around step 100.
		lat, lng := 37.3383, -121.8864
		if i > 100 { lat, lng = 37.50, -121.99 } // now outside
		for _, tr := range monitor.Update(r.VehicleID, geofence.Point{Lat: lat, Lng: lng}) {
			store.Append(storage.Record{Type: "geofence", Vehicle: tr.Vehicle, Payload: tr.Zone + " " + tr.Event})
			broker.Publish("GEOFENCE " + tr.Vehicle + " " + tr.Event + " " + tr.Zone)
		}
	}

	log.Printf("window size=%d avg_speed=%.1f idle=%d", window.Size(), window.AvgSpeed(), window.IdleCount())
	log.Printf("stored operational records: %d", store.Count())
}
