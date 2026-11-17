package match

// Domain events: facts the match produces. The application layer decides how
// (and to whom) they are delivered.
type Event interface{ isEvent() }

type PlayerJoined struct {
	ID   PlayerID
	Name string
}

type PlayerLeft struct{ ID PlayerID }

type PlayerKilled struct{ Killer, Victim PlayerID }

type PlayerRespawned struct {
	ID   PlayerID
	X, Y float32
}

func (PlayerJoined) isEvent()    {}
func (PlayerLeft) isEvent()      {}
func (PlayerKilled) isEvent()    {}
func (PlayerRespawned) isEvent() {}
