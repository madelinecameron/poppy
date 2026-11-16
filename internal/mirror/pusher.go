package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/madelinecameron/poppy/internal/gitx"
	"github.com/madelinecameron/poppy/internal/snapshot"
)

// DefaultRefresh is how long a Pusher trusts that an unchanged repository is
// still on its mirror before pushing it again anyway.
const DefaultRefresh = time.Hour

// Pusher pushes snapshot refs for a long-running process and remembers what
// each repository last pushed. Each push costs one SSH session on the mirror
// host, and creating the mirror costs another, so a daemon that pushed every
// dirty repository every cycle would hold the host in a constant stream of
// logins. Pusher skips a repository whose snapshot refs have not moved since
// its last successful push, and creates a mirror only on the first push to it
// or after a push to it has failed.
//
// A Pusher is safe for concurrent use.
type Pusher struct {
	// Refresh bounds how long an unchanged repository goes without a push, so
	// a mirror that was removed or rewritten on the host is repopulated.
	Refresh time.Duration

	now  func() time.Time
	mu   sync.Mutex
	last map[string]pushRecord
}

type pushRecord struct {
	digest string
	at     time.Time
}

// NewPusher returns a Pusher with the default refresh interval.
func NewPusher() *Pusher {
	return &Pusher{Refresh: DefaultRefresh, now: time.Now, last: map[string]pushRecord{}}
}

// Push sends repoPath's snapshot refs for machine to m unless they are
// unchanged since the last successful push within the refresh interval. It
// reports whether it pushed.
func (p *Pusher) Push(ctx context.Context, m *Mirror, repoPath, machine string) (bool, error) {
	key := m.url() + "\x00" + repoPath
	digest, digestErr := refsDigest(ctx, repoPath, machine)
	now := p.now()

	p.mu.Lock()
	prev, known := p.last[key]
	p.mu.Unlock()

	if known && digestErr == nil && prev.digest == digest && now.Sub(prev.at) < p.Refresh {
		return false, nil
	}

	var err error
	if known {
		err = m.pushRefs(ctx, repoPath, machine)
	} else {
		err = m.Push(ctx, repoPath, machine)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		delete(p.last, key)
		return true, err
	}
	if digestErr != nil {
		digest = ""
	}
	p.last[key] = pushRecord{digest: digest, at: now}
	return true, nil
}

// refsDigest fingerprints the machine's snapshot refs in a repository, which
// is exactly what Push sends.
func refsDigest(ctx context.Context, repoPath, machine string) (string, error) {
	out, err := gitx.New(repoPath).Run(ctx, "for-each-ref", "--format=%(objectname) %(refname)",
		snapshot.LatestPrefix+"/"+machine+"/",
		snapshot.HistoryPrefix+"/"+machine+"/")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(out))
	return hex.EncodeToString(sum[:]), nil
}
