package node

import (
	"fmt"
	"math"
	"sort"

	"github.com/btcsuite/btcd/claimtrie/change"
	"github.com/btcsuite/btcd/claimtrie/param"
)

// ErrNotFound is returned when a claim or support is not found.
var mispents = map[string]bool{}

type Node struct {
	BestClaim   *Claim    // The claim that has most effective amount at the current height.
	TakenOverAt int32     // The height at when the current BestClaim took over.
	Claims      ClaimList // List of all Claims.
	Supports    ClaimList // List of all Supports, including orphaned ones.
}

// New returns a new node.
func New() *Node {
	return &Node{}
}

func (n *Node) ApplyChange(chg change.Change, delay int32) error {

	out := NewOutPointFromString(chg.OutPoint)

	visibleAt := chg.VisibleHeight
	if visibleAt <= 0 {
		visibleAt = chg.Height
	}

	switch chg.Type {
	case change.AddClaim:
		c := &Claim{
			OutPoint:   *out,
			Amount:     chg.Amount,
			ClaimID:    chg.ClaimID,
			AcceptedAt: chg.Height, // not tracking original height in this version (but we could)
			ActiveAt:   chg.Height + delay,
			Value:      chg.Value,
			VisibleAt:  visibleAt,
		}
		old := n.Claims.find(byOut(*out)) // TODO: remove this after proving ResetHeight works
		if old != nil {
			fmt.Printf("CONFLICT WITH EXISTING TXO! Name: %s, Height: %d\n", chg.Name, chg.Height)
		}
		n.Claims = append(n.Claims, c)

	case change.SpendClaim:
		c := n.Claims.find(byOut(*out))
		if c != nil {
			c.setStatus(Deactivated)
		} else if !mispents[fmt.Sprintf("%d_%s", chg.Height, chg.ClaimID)] {
			mispents[fmt.Sprintf("%d_%s", chg.Height, chg.ClaimID)] = true
			fmt.Printf("Spending claim but missing existing claim with TXO %s\n   "+
				"Name: %s, ID: %s\n", chg.OutPoint, chg.Name, chg.ClaimID)
		}
		// apparently it's legit to be absent in the map:
		// 'two' at 481100, 36a719a156a1df178531f3c712b8b37f8e7cc3b36eea532df961229d936272a1:0

	case change.UpdateClaim:
		// Find and remove the claim, which has just been spent.
		c := n.Claims.find(byID(chg.ClaimID))
		if c != nil && c.Status == Deactivated {

			// Keep its ID, which was generated from the spent claim.
			// And update the rest of properties.
			c.setOutPoint(*out).SetAmt(chg.Amount).SetValue(chg.Value)
			c.setStatus(Accepted) // it was Deactivated in the spend

			// It's a bug, but the old code would update these.
			// That forces this to be newer, which may in an unintentional takeover if there's an older one.
			c.setAccepted(chg.Height)         // TODO: Fork this out
			c.setActiveAt(chg.Height + delay) // TODO: Fork this out

		} else {
			fmt.Printf("Updating claim but missing existing claim with ID %s", chg.ClaimID)
		}
	case change.AddSupport:
		n.Supports = append(n.Supports, &Claim{
			OutPoint:   *out,
			Amount:     chg.Amount,
			ClaimID:    chg.ClaimID,
			AcceptedAt: chg.Height,
			Value:      chg.Value,
			ActiveAt:   chg.Height + delay,
			VisibleAt:  visibleAt,
		})

	case change.SpendSupport:
		s := n.Supports.find(byOut(*out))
		if s != nil {
			s.setStatus(Deactivated)
		} else {
			fmt.Printf("Spending support but missing existing support with TXO %s\n   "+
				"Name: %s, ID: %s\n", chg.OutPoint, chg.Name, chg.ClaimID)
		}
	}
	return nil
}

// AdjustTo activates claims and computes takeovers until it reaches the specified height.
func (n *Node) AdjustTo(height, maxHeight int32, name []byte) *Node {
	changed := n.handleExpiredAndActivated(height) > 0
	n.updateTakeoverHeight(height, name, changed)
	if maxHeight > height {
		for h := n.NextUpdate(); h <= maxHeight; h = n.NextUpdate() {
			changed = n.handleExpiredAndActivated(h) > 0
			n.updateTakeoverHeight(h, name, changed)
			height = h
		}
	}
	return n
}

func (n *Node) updateTakeoverHeight(height int32, name []byte, refindBest bool) {

	candidate := n.BestClaim
	if refindBest {
		candidate = n.findBestClaim() // so expensive...
	}

	hasCandidate := candidate != nil
	hasCurrentWinner := n.BestClaim != nil && n.BestClaim.Status == Activated

	takeoverHappening := !hasCandidate || !hasCurrentWinner || candidate.ClaimID != n.BestClaim.ClaimID

	if takeoverHappening {
		if n.activateAllClaims(height) > 0 {
			candidate = n.findBestClaim()
		}
	}

	if !takeoverHappening && height < param.MaxRemovalWorkaroundHeight {
		// This is a super ugly hack to work around bug in old code.
		// The bug: un/support a name then update it. This will cause its takeover height to be reset to current.
		// This is because the old code would add to the cache without setting block originals when dealing in supports.
		_, takeoverHappening = param.TakeoverWorkarounds[fmt.Sprintf("%d_%s", height, name)] // TODO: ditch the fmt call
	}

	if takeoverHappening {
		n.TakenOverAt = height
		n.BestClaim = candidate
	}
}

func (n *Node) handleExpiredAndActivated(height int32) int {

	changes := 0
	update := func(items ClaimList) ClaimList {
		for i := 0; i < len(items); i++ {
			c := items[i]
			if c.Status == Accepted && c.ActiveAt <= height && c.VisibleAt <= height {
				c.setStatus(Activated)
				changes++
			}
			if c.ExpireAt() <= height || c.Status == Deactivated {
				if i < len(items)-1 {
					items[i] = items[len(items)-1]
					i--
				}
				items = items[:len(items)-1]
				changes++
			}
		}
		return items
	}
	n.Claims = update(n.Claims)
	n.Supports = update(n.Supports)
	return changes
}

// NextUpdate returns the nearest height in the future that the node should
// be refreshed due to changes of claims or supports.
func (n Node) NextUpdate() int32 {

	next := int32(math.MaxInt32)

	for _, c := range n.Claims {
		if c.ExpireAt() < next {
			next = c.ExpireAt()
		}
		// if we're not active, we need to go to activeAt unless we're still invisible there
		if c.Status == Accepted {
			min := c.ActiveAt
			if c.VisibleAt > min {
				min = c.VisibleAt
			}
			if min < next {
				next = min
			}
		}
	}

	for _, s := range n.Supports {
		if s.ExpireAt() < next {
			next = s.ExpireAt()
		}
		if s.Status == Accepted {
			min := s.ActiveAt
			if s.VisibleAt > min {
				min = s.VisibleAt
			}
			if min < next {
				next = min
			}
		}
	}

	return next
}

func (n Node) findBestClaim() *Claim {

	// WARNING: this method is called billions of times.
	// if we just had some easy way to know that our best claim was the first one in the list...
	// or it may be faster to cache effective amount in the db at some point.

	var best *Claim
	var bestAmount int64
	for _, candidate := range n.Claims {

		// not using switch here for performance reasons
		if candidate.Status != Activated {
			continue
		}

		if best == nil {
			best = candidate
			continue
		}

		candidateAmount := candidate.EffectiveAmount(n.Supports)
		if bestAmount <= 0 { // trying to reduce calls to EffectiveAmount
			bestAmount = best.EffectiveAmount(n.Supports)
		}

		switch {
		case candidateAmount > bestAmount:
			best = candidate
			bestAmount = candidateAmount
		case candidateAmount < bestAmount:
			continue
		case candidate.AcceptedAt < best.AcceptedAt:
			best = candidate
			bestAmount = candidateAmount
		case candidate.AcceptedAt > best.AcceptedAt:
			continue
		case OutPointLess(candidate.OutPoint, best.OutPoint):
			best = candidate
			bestAmount = candidateAmount
		}
	}

	return best
}

func (n *Node) activateAllClaims(height int32) int {
	count := 0
	for _, c := range n.Claims {
		if c.Status == Accepted && c.ActiveAt > height && c.VisibleAt <= height {
			c.setActiveAt(height) // don't necessary need to change this number
			c.setStatus(Activated)
			count++
		}
	}

	for _, s := range n.Supports {
		if s.Status == Accepted && s.ActiveAt > height && s.VisibleAt <= height {
			s.setActiveAt(height) // don't necessary need to change this number
			s.setStatus(Activated)
			count++
		}
	}
	return count
}

func (n *Node) SortClaims() {

	// purposefully sorting by descent
	sort.Slice(n.Claims, func(j, i int) bool {
		iAmount := n.Claims[i].EffectiveAmount(n.Supports)
		jAmount := n.Claims[j].EffectiveAmount(n.Supports)
		switch {
		case iAmount < jAmount:
			return true
		case iAmount > jAmount:
			return false
		case n.Claims[i].AcceptedAt > n.Claims[j].AcceptedAt:
			return true
		case n.Claims[i].AcceptedAt < n.Claims[j].AcceptedAt:
			return false
		}
		return OutPointLess(n.Claims[j].OutPoint, n.Claims[i].OutPoint)
	})
}
