package match

import (
	"errors"
	"math"
	"math/rand/v2"
	"sort"
)

type PlayerID = uint8

var ErrAlreadyJoined = errors.New("match: player already joined")

// Input is a player command for one 1/60 s step.
type Input struct {
	Seq          uint32
	MoveX, MoveY int8
	Aim          float32
	Shoot        bool
}

// Player is an entity inside the Match aggregate.
type Player struct {
	ID            PlayerID
	Name          string
	X, Y, Angle   float32
	HP            uint8
	Alive         bool
	Kills, Deaths uint16
	LastSeq       uint32 // last applied input; clients reconcile against it

	lastQueued  uint32
	haveQueued  bool
	inputs      []Input
	nextFire    uint32
	respawnTick uint32
}

type Bullet struct {
	ID      uint16
	Owner   PlayerID
	X, Y    float32
	VX, VY  float32
	DieTick uint32
}

// Match is the aggregate root. All changes go through its methods, which
// enforce the rules and return the resulting domain events.
type Match struct {
	tick       uint32
	players    map[PlayerID]*Player
	bullets    []*Bullet
	nextBullet uint16
	walls      []Wall
	rng        *rand.Rand
	events     []Event
}

func New(walls []Wall, seed uint64) *Match {
	return &Match{
		players: map[PlayerID]*Player{},
		walls:   walls,
		rng:     rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
	}
}

func (m *Match) Walls() []Wall        { return m.walls }
func (m *Match) CurrentTick() uint32  { return m.tick }
func (m *Match) Has(id PlayerID) bool { _, ok := m.players[id]; return ok }

// Players returns copies sorted by id.
func (m *Match) Players() []Player {
	out := make([]Player, 0, len(m.players))
	for _, p := range m.sorted() {
		out = append(out, *p)
	}
	return out
}

func (m *Match) Bullets() []Bullet {
	out := make([]Bullet, len(m.bullets))
	for i, b := range m.bullets {
		out[i] = *b
	}
	return out
}

func (m *Match) Player(id PlayerID) (Player, bool) {
	p, ok := m.players[id]
	if !ok {
		return Player{}, false
	}
	return *p, true
}

func (m *Match) sorted() []*Player {
	ps := make([]*Player, 0, len(m.players))
	for _, p := range m.players {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID })
	return ps
}

func (m *Match) emit(e Event) { m.events = append(m.events, e) }

func (m *Match) flush() []Event {
	ev := m.events
	m.events = nil
	return ev
}

func (m *Match) Join(id PlayerID, name string) ([]Event, error) {
	if m.Has(id) {
		return nil, ErrAlreadyJoined
	}
	p := &Player{ID: id, Name: name, HP: MaxHP, Alive: true}
	p.X, p.Y = m.spawnPoint()
	m.players[id] = p
	m.emit(PlayerJoined{ID: id, Name: name})
	return m.flush(), nil
}

func (m *Match) Leave(id PlayerID) []Event {
	if !m.Has(id) {
		return nil
	}
	delete(m.players, id)
	m.emit(PlayerLeft{ID: id})
	return m.flush()
}

// QueueInput validates and queues an input. Out-of-order or duplicate seqs
// are rejected; the queue keeps only the newest MaxQueuedInputs.
func (m *Match) QueueInput(id PlayerID, in Input) bool {
	p := m.players[id]
	if p == nil || (p.haveQueued && in.Seq <= p.lastQueued) {
		return false
	}
	p.lastQueued, p.haveQueued = in.Seq, true
	p.inputs = append(p.inputs, sanitize(in))
	if len(p.inputs) > MaxQueuedInputs {
		p.inputs = append(p.inputs[:0], p.inputs[len(p.inputs)-MaxQueuedInputs:]...)
	}
	return true
}

func sanitize(in Input) Input {
	clamp := func(v int8) int8 { return max(-1, min(1, v)) }
	in.MoveX, in.MoveY = clamp(in.MoveX), clamp(in.MoveY)
	if a := float64(in.Aim); math.IsNaN(a) || math.IsInf(a, 0) {
		in.Aim = 0
	}
	return in
}

// Step advances the match by one tick.
func (m *Match) Step() []Event {
	m.tick++
	for _, p := range m.sorted() {
		n := min(len(p.inputs), MaxInputsPerTick)
		for _, in := range p.inputs[:n] {
			p.LastSeq = in.Seq
			if !p.Alive {
				continue
			}
			p.X, p.Y = MovePlayer(p.X, p.Y, in.MoveX, in.MoveY, m.walls)
			p.Angle = in.Aim
			if in.Shoot && m.tick >= p.nextFire {
				m.fire(p)
				p.nextFire = m.tick + FireCooldownTicks
			}
		}
		p.inputs = append(p.inputs[:0], p.inputs[n:]...)
		if !p.Alive && m.tick >= p.respawnTick {
			p.HP, p.Alive = MaxHP, true
			p.X, p.Y = m.spawnPoint()
			m.emit(PlayerRespawned{ID: p.ID, X: p.X, Y: p.Y})
		}
	}
	m.stepBullets()
	return m.flush()
}

func (m *Match) spawnPoint() (float32, float32) {
	margin := PlayerRadius + 20
	var x, y float32
	for try := 0; try < 100; try++ {
		x = margin + m.rng.Float32()*(MapW-2*margin)
		y = margin + m.rng.Float32()*(MapH-2*margin)
		if HitsAnyWall(x, y, PlayerRadius+8, m.walls) {
			continue
		}
		crowded := false
		for _, o := range m.players {
			if o.Alive && sq(o.X-x)+sq(o.Y-y) < 150*150 {
				crowded = true
				break
			}
		}
		if !crowded || try > 80 {
			return x, y
		}
	}
	return x, y
}

func (m *Match) fire(p *Player) {
	s, c := math.Sincos(float64(p.Angle))
	dx, dy := float32(c), float32(s)
	off := PlayerRadius + BulletRadius + 2
	b := &Bullet{
		ID: m.nextBullet, Owner: p.ID,
		X: p.X + dx*off, Y: p.Y + dy*off,
		VX: dx * BulletSpeed, VY: dy * BulletSpeed,
		DieTick: m.tick + BulletLifeTicks,
	}
	m.nextBullet++
	if !HitsAnyWall(b.X, b.Y, BulletRadius, m.walls) {
		m.bullets = append(m.bullets, b)
	}
}

const bulletSubsteps = 2

func (m *Match) stepBullets() {
	ps := m.sorted()
	hitR := sq(PlayerRadius + BulletRadius)
	kept := m.bullets[:0]
	for _, b := range m.bullets {
		if m.tick >= b.DieTick {
			continue
		}
		hit := false
		for s := 0; s < bulletSubsteps && !hit; s++ {
			b.X += b.VX / (TickRate * bulletSubsteps)
			b.Y += b.VY / (TickRate * bulletSubsteps)
			if b.X < 0 || b.Y < 0 || b.X > MapW || b.Y > MapH || HitsAnyWall(b.X, b.Y, BulletRadius, m.walls) {
				hit = true
				break
			}
			for _, p := range ps {
				if p.Alive && p.ID != b.Owner && sq(p.X-b.X)+sq(p.Y-b.Y) < hitR {
					m.damage(p, b.Owner)
					hit = true
					break
				}
			}
		}
		if !hit {
			kept = append(kept, b)
		}
	}
	for i := len(kept); i < len(m.bullets); i++ {
		m.bullets[i] = nil
	}
	m.bullets = kept
}

func (m *Match) damage(p *Player, owner PlayerID) {
	if p.HP > Damage {
		p.HP -= Damage
		return
	}
	p.HP, p.Alive = 0, false
	p.Deaths++
	p.respawnTick = m.tick + RespawnTicks
	p.inputs = p.inputs[:0]
	if k := m.players[owner]; k != nil {
		k.Kills++
	}
	m.emit(PlayerKilled{Killer: owner, Victim: p.ID})
}
