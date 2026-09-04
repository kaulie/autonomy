package autonomy

// Agent decides the next step for a task given the current world.
type Agent interface {
	Next(task Task, world World) (Step, error)
}
