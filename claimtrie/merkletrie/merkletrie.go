package merkletrie

import (
	"bytes"
	"sync"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/cockroachdb/pebble"
)

var (
	// emptyTrieHash represents the Merkle Hash of an empty MerkleTrie.
	// "0000000000000000000000000000000000000000000000000000000000000001"
	emptyTrieHash = &chainhash.Hash{1}
)

// ValueStore enables MerkleTrie to query node values from different implementations.
type ValueStore interface {
	Get(name []byte) (*chainhash.Hash, error)
}

// MerkleTrie implements a 256-way prefix tree.
type MerkleTrie struct {
	store ValueStore
	repo  Repo

	root *node
	bufs *sync.Pool
}

// New returns a MerkleTrie.
func New(store ValueStore, repo Repo) *MerkleTrie {

	tr := &MerkleTrie{
		store: store,
		repo:  repo,
		bufs: &sync.Pool{
			New: func() interface{} {
				return new(bytes.Buffer)
			},
		},
	}

	tr.SetRoot(emptyTrieHash)

	return tr
}

// SetRoot drops all resolved nodes in the MerkleTrie, and set the root with specified hash.
func (t *MerkleTrie) SetRoot(h *chainhash.Hash) {
	t.root = newNode()
	t.root.hash = h
}

// Update updates the nodes along the path to the key.
// Each node is resolved or created with their Hash cleared.
func (t *MerkleTrie) Update(key []byte) {

	n := t.root
	for _, ch := range key {
		t.resolve(n)
		if n.links[ch] == nil {
			n.links[ch] = newNode()
		}
		n.hash = nil
		n = n.links[ch]
	}

	t.resolve(n)
	n.hasValue = true
	n.hash = nil
}

func (t *MerkleTrie) resolve(n *node) {

	if n.hash == nil {
		return
	}

	b, closer, err := t.repo.Get(n.hash[:])
	if err == pebble.ErrNotFound {
		return
	} else if err != nil {
		panic(err)
	}
	defer closer.Close()

	nb := nbuf(b)
	n.hasValue = nb.hasValue()
	for i := 0; i < nb.entries(); i++ {
		p, h := nb.entry(i)
		n.links[p] = newNode()
		n.links[p].hash = h
	}
}

// MerkleHash returns the Merkle Hash of the MerkleTrie.
// All nodes must have been resolved before calling this function.
func (t *MerkleTrie) MerkleHash() *chainhash.Hash {
	buf := make([]byte, 0, 4096)
	if h := t.merkle(buf, t.root); h == nil {
		return emptyTrieHash
	}
	return t.root.hash
}

// merkle recursively resolves the hashes of the node.
// All nodes must have been resolved before calling this function.
func (t *MerkleTrie) merkle(prefix []byte, n *node) *chainhash.Hash {
	if n.hash != nil {
		return n.hash
	}
	b := t.bufs.Get().(*bytes.Buffer)
	defer t.bufs.Put(b)
	b.Reset()

	for ch, n := range n.links {
		if n == nil {
			continue
		}
		p := append(prefix, byte(ch))
		if h := t.merkle(p, n); h != nil {
			b.WriteByte(byte(ch)) // nolint : errchk
			b.Write(h[:])         // nolint : errchk
		}
	}

	if n.hasValue {
		h, err := t.store.Get(prefix)
		if err != nil {
			return nil
		}
		if h != nil {
			b.Write(h[:]) // nolint : errchk
		}
	}

	if b.Len() != 0 {
		h := chainhash.DoubleHashH(b.Bytes())
		n.hash = &h
		t.repo.Set(h[:], b.Bytes())
	}

	return n.hash
}

func (t *MerkleTrie) Close() error {
	return t.repo.Close()
}
