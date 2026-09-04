package autonomy

// Asset is something in the world that can be observed, referenced, or changed.
type Asset struct {
	ID    string
	Kind  string
	State string
}
