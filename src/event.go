package autonomy

import "time"

// Event is an observable fact produced by the loop.
type Event struct {
	ID        string
	Timestamp time.Time
	Type      string
	Message   string
	Data      map[string]interface{}
	Asset     Asset //optional asset that is affected by the event
}
