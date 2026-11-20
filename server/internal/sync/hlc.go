// Package sync holds the sync building blocks. hlc.go implements a
// Hybrid Logical Clock: monotonic, wall-clock-close timestamps that are
// totally ordered across all devices and sort lexicographically.
package sync

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Stamp format: "<ms:15>:<counter:5>:<nodeID>", fixed width so string order
// equals time order. app/src/lib/local/hlc.ts implements the same rules.
const (
	msDigits      = 15
	counterDigits = 5
	maxCounter    = 99999
	maxNodeLen    = 64
)

// MaxDrift is how far ahead of the receiver's wall clock a stamp may be.
// Stamps beyond it are refused: one device with a wrong clock would otherwise
// win every per-field merge until real time caught up with it.
const MaxDrift = 10 * time.Minute

// ErrMalformed reports a stamp that does not follow the fixed-width format.
var ErrMalformed = errors.New("malformed hlc stamp")

type HLC struct {
	mu      sync.Mutex
	lastMS  int64
	counter int64
	nodeID  string
}

func NewHLC(nodeID string) *HLC { return &HLC{nodeID: nodeID} }

func nowMS() int64 { return time.Now().UnixMilli() }

// Now returns a fresh local timestamp (for local writes).
func (h *HLC) Now() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if pt := nowMS(); pt > h.lastMS {
		h.lastMS, h.counter = pt, 0
	} else {
		h.tick()
	}
	return Format(h.lastMS, h.counter, h.nodeID)
}

// Recv folds a foreign stamp into the clock, so that every stamp issued
// afterwards is greater than it. A malformed stamp is refused and leaves the
// clock untouched.
func (h *HLC) Recv(remote string) error {
	rms, rc, _, ok := Parse(remote)
	if !ok {
		return ErrMalformed
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	pt := nowMS()
	switch {
	case pt > h.lastMS && pt > rms:
		h.lastMS, h.counter = pt, 0
	case rms > h.lastMS:
		h.lastMS, h.counter = rms, rc
		h.tick()
	case h.lastMS > rms:
		h.tick()
	default:
		if rc > h.counter {
			h.counter = rc
		}
		h.tick()
	}
	return nil
}

// tick advances the counter. The counter has a fixed width, so when it is
// exhausted the millisecond moves on instead: a sixth digit would break the
// string ordering.
func (h *HLC) tick() {
	if h.counter >= maxCounter {
		h.lastMS, h.counter = h.lastMS+1, 0
		return
	}
	h.counter++
}

// Format renders a stamp.
func Format(ms, counter int64, node string) string {
	return fmt.Sprintf("%0*d:%0*d:%s", msDigits, ms, counterDigits, counter, node)
}

// Parse splits a stamp and reports whether it is well-formed: exactly 15
// digits, ':', exactly 5 digits, ':', and a node id of 1-64 characters out of
// [A-Za-z0-9._-].
func Parse(s string) (ms, counter int64, node string, ok bool) {
	const nodeAt = msDigits + 1 + counterDigits + 1
	if len(s) <= nodeAt || len(s) > nodeAt+maxNodeLen ||
		s[msDigits] != ':' || s[nodeAt-1] != ':' {
		return 0, 0, "", false
	}
	ms, okMS := digits(s[:msDigits])
	counter, okC := digits(s[msDigits+1 : nodeAt-1])
	node = s[nodeAt:]
	if !okMS || !okC || !validNode(node) {
		return 0, 0, "", false
	}
	return ms, counter, node, true
}

// Valid reports whether s is a well-formed stamp.
func Valid(s string) bool {
	_, _, _, ok := Parse(s)
	return ok
}

// AheadBy returns how far the stamp's wall-clock part lies after now
// (negative when it is in the past); ok is false for a malformed stamp.
func AheadBy(stamp string, now time.Time) (d time.Duration, ok bool) {
	ms, _, _, ok := Parse(stamp)
	if !ok {
		return 0, false
	}
	return time.Duration(ms-now.UnixMilli()) * time.Millisecond, true
}

func digits(s string) (int64, bool) {
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}

func validNode(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return s != ""
}

// Compare: -1 a<b, 0 equal, 1 a>b. Well-formed stamps compare as strings
// (fixed width). A malformed stamp counts as "no stamp" and sorts below
// every well-formed one, so garbage stored in a field clock can never win a
// merge or block a real write.
func Compare(a, b string) int {
	va, vb := Valid(a), Valid(b)
	switch {
	case va && vb:
		return strings.Compare(a, b)
	case va:
		return 1
	case vb:
		return -1
	}
	return 0
}
