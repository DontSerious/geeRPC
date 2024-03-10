package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

type SelectMode int

const (
	RandomSelect     SelectMode = iota // select randomly
	RoundRobinSelect                   // select using Robin algorithm
)

type Discovery interface {
	Refresh() error                      // refresh service list from remote registry
	Update(server []string) error        // manual update service list
	Get(mode SelectMode) (string, error) // select a service instance according to mode
	GetAll() ([]string, error)
}

// MultiServiceDiscovery is a discovery for multi servers without a registry center.
// user provides the server addresses explicitly instead.
type MultiServiceDiscovery struct {
	r       *rand.Rand   // generate random number
	mu      sync.RWMutex // protect following
	servers []string
	index   int // record the selected position for robin algorithm
}

func NewMultiServiceDiscovery(servers []string) *MultiServiceDiscovery {
	d := &MultiServiceDiscovery{
		servers: servers,
		r:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	d.index = d.r.Intn(math.MaxInt32 - 1)
	return d
}

var _ Discovery = (*MultiServiceDiscovery)(nil)

// Refresh does't make sense for MultiServiceDiscovery, so ignore it.
func (d *MultiServiceDiscovery) Refresh() error {
	return nil
}

// Update the servers of discovery dynamically if needed.
func (d *MultiServiceDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	return nil
}

// Get a server according to mode.
func (d *MultiServiceDiscovery) Get(mode SelectMode) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	n := len(d.servers)
	if n == 0 {
		return "", errors.New("rpc discovery: no available servers")
	}

	switch mode {
	case RandomSelect:
		return d.servers[d.r.Intn(n)], nil
	case RoundRobinSelect:
		s := d.servers[d.index%n]
		d.index = (d.index + 1) % n
		return s, nil
	default:
		return "", errors.New("rpc discovery: not supported select mode")
	}
}

func (d *MultiServiceDiscovery) GetAll() ([]string, error) {
	// RLock(): Multiple go routines can be read (not written) simultaneously by acquiring a lock.
	d.mu.RLock()
	defer d.mu.RUnlock()

	// return a copy of d.servers
	servers := make([]string, len(d.servers))
	copy(servers, d.servers)
	return servers, nil
}
