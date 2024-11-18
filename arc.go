// Copyright Chrono Technologies LLC
// SPDX-License-Identifier: MIT

// Package arc implements a key-value database based on a Radix tree data
// structure and deduplication-enabled blob storage. The Radix tree provides
// space-efficient key management through prefix compression, while the blob
// storage handles values with automatic deduplication.
package arc

import (
	"errors"
	"sync"
)

var (
	// ErrCorrupted is returned when a database corruption is detected.
	ErrCorrupted = errors.New("database corruption detected")

	// ErrKeyNotFound is returned when the key does not exist in the index.
	ErrKeyNotFound = errors.New("key not found")

	// ErrKeyTooLarge is returned when the key size exceeds the 64KB limit.
	ErrKeyTooLarge = errors.New("key is too large")

	// ErrNilKey is returned when an insertion is attempted using a nil key.
	ErrNilKey = errors.New("key cannot be nil")

	// ErrValueTooLarge is returned when the value size exceeds the 4GB limit.
	ErrValueTooLarge = errors.New("value is too large")
)

const (
	maxUint16     = (1 << 16) - 1 // maxUint16 is the maximum value of uint16.
	maxUint32     = (1 << 32) - 1 // maxUint32 is the maximum value of uint32.
	maxKeyBytes   = maxUint16     // maxKeyBytes is the maximum key size.
	maxValueBytes = maxUint32     // maxValueBytes is the maximum value size.
)

// Arc represents the API interface of a space-efficient key-value database that
// combines a Radix tree for key indexing and a space-optimized blob store.
type Arc struct {
	root       *node        // Pointer to the root node.
	numNodes   int          // Number of nodes in the tree.
	numRecords int          // Number of records in the tree.
	mu         sync.RWMutex // RWLock for concurrency management.
}

// New returns an empty Arc database handler.
func New() *Arc {
	return &Arc{}
}

// Len returns the number of records.
func (a *Arc) Len() int {
	a.mu.RLock()
	defer a.mu.RUnlock()

	return a.numRecords
}

func (a *Arc) empty() bool {
	return a.root == nil && a.numRecords == 0
}

// Put inserts or updates a key-value pair in the database. It returns an error
// if the key is nil or if either the key or value exceeds size limits.
func (a *Arc) Put(key []byte, value []byte) error {
	if key == nil {
		return ErrNilKey
	}

	if len(key) > maxKeyBytes {
		return ErrKeyTooLarge
	}

	if len(value) > maxValueBytes {
		return ErrValueTooLarge
	}

	newNode := &node{}
	newNode.setKey(key)
	newNode.setValue(value)

	a.mu.Lock()
	defer a.mu.Unlock()

	// Empty empty: Set newNode as the root.
	if a.empty() {
		a.root = newNode
		a.numNodes = 1
		a.numRecords = 1

		return nil
	}

	// Given key does not share a common prefix with the existing root node
	// that holds a non-nil key. Make newNode and the current root siblings
	// under a new nil-key root node whose purpose is to group top-level keys.
	if len(a.root.key) > 0 && longestCommonPrefix(a.root.key, key) == nil {
		oldRoot := a.root

		a.root = &node{key: nil}
		a.root.addChild(oldRoot)
		a.root.addChild(newNode)

		a.numNodes += 2
		a.numRecords++

		return nil
	}

	var parent *node
	var current = a.root

	for {
		prefix := longestCommonPrefix(current.key, key)
		prefixLen := len(prefix)

		// Found exact match. Put() will overwrite the existing value.
		// Do not update counters because this is an in-place update.
		if prefixLen == len(current.key) && prefixLen == len(newNode.key) {
			if !current.isRecord {
				a.numRecords++
			}

			current.setValue(value)

			return nil
		}

		// The longest common prefix matches all of newNode's key but is shorter
		// than current's key. Therefore, newNode becomes the parent of current.
		//
		// For example, suppose newNode.key is "app" and current.key is "apple".
		// The longest common prefix is "app". Therefore "apple" is updated to
		// "le", and then becomes a child of "app" (newNode), forming the path:
		// ["app"(newNode) -> "le"(current)].
		if prefixLen == len(newNode.key) && prefixLen < len(current.key) {
			// If the current node is root, then all we need to do is set
			// newNode as the root. Otherwise replace current with newNode
			// within the parent's child linked-list.
			if current == a.root {
				current.setKey(current.key[len(newNode.key):])
				newNode.addChild(current)
				a.root = newNode
			} else {
				if err := parent.removeChild(current); err != nil {
					return err
				}

				current.setKey(current.key[len(newNode.key):])
				newNode.addChild(current)
				parent.addChild(newNode)
			}

			a.numNodes++
			a.numRecords++

			return nil
		}

		// Partial match with key exhaustion: Insert via node splitting.
		if prefixLen > 0 && prefixLen < len(current.key) {
			a.splitNode(parent, current, newNode, prefix)
			return nil
		}

		// Search for a child whose key is compatible with the remaining
		// portion of the key. Start by removing the prefix from the key.
		key = key[prefixLen:]
		nextNode := current.findCompatibleChild(key)

		newNode.setKey(newNode.key[prefixLen:])

		// Reached the deepest level of the tree for the given key.
		if nextNode == nil {
			if current == a.root {
				if a.root.key == nil || prefixLen == len(a.root.key) {
					a.root.addChild(newNode)
					a.numNodes++
				} else {
					// Make current and newNode siblings by creating a new root.
					a.root = &node{key: prefix}
					a.root.addChild(current)
					a.root.addChild(newNode)

					// Increment by 2 for the new root node and newNode.
					a.numNodes += 2
				}
			} else {
				// Simple case where newNode becomes a child of the leaf node.
				current.addChild(newNode)
				a.numNodes++
			}

			a.numRecords++
			return nil
		}

		// Reaching this point means that a compatible child was found.
		// Update relevant iterators and continue traversing the tree until
		// we reach a leaf node or no further nodes are available.
		parent = current
		current = nextNode
	}
}

// Get retrieves the value that matches the given key. Returns ErrKeyNotFound
// if the key does not exist.
func (a *Arc) Get(key []byte) ([]byte, error) {
	if key == nil {
		return nil, ErrNilKey
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	node, _, err := a.findNodeAndParent(key)

	if err != nil {
		return nil, err
	}

	if !node.isRecord {
		return nil, ErrKeyNotFound
	}

	return node.data, nil
}

// Delete removes a record that matches the given key.
func (a *Arc) Delete(key []byte) error {
	if key == nil {
		return ErrNilKey
	}

	if a.empty() {
		return ErrKeyNotFound
	}

	if len(key) > maxKeyBytes {
		return ErrKeyTooLarge
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	delNode, parent, err := a.findNodeAndParent(key)

	if err != nil {
		return err
	}

	if !delNode.isRecord {
		return ErrKeyNotFound
	}

	// Root node deletion is handled separately to improve code readability.
	if delNode == a.root {
		a.deleteRootNode()
		return nil
	}

	// If the deletion node is not a root node, its parent must be non-nil.
	if parent == nil {
		return ErrCorrupted
	}

	return nil
}

// deleteRootNode removes the root node from the tree, while ensuring that
// the tree structure remains valid and consistent.
func (a *Arc) deleteRootNode() {
	if a.root.isLeaf() {
		a.clear()
		return
	}

	if a.root.numChildren == 1 {
		// The root node only has one child, which will become the new root.
		child := a.root.firstChild
		child.prependKey(a.root.key)

		a.root = child

		// Decrement for the original root node removal.
		a.numNodes--

	} else {
		// The root node has multiple children, thus it must continue to exist
		// for the tree to sustain its structure. Convert it to a non-record
		// node by removing its value and flagging it as a non-record node.
		a.root.isRecord = false
		a.root.data = nil
	}

	a.numRecords--
}

// Clear wipes the database from memory.
func (a *Arc) Clear() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.clear()
}

// clear wipes the in-memory tree, and resets metadata.
func (a *Arc) clear() {
	a.root = nil
	a.numNodes = 0
	a.numRecords = 0
}

// splitNode splits a node based on a common prefix by creating an intermediate
// parent node. For the root node, it simply creates a new parent. For non-root
// nodes, it updates the parent-child relationships before modifying the node
// keys to maintain tree consistency. The current and newNode becomes children
// of the intermediate parent, with their keys updated to contain only their
// suffixes after the common prefix.
func (a *Arc) splitNode(parent *node, current *node, newNode *node, commonPrefix []byte) {
	newParent := &node{key: commonPrefix}

	// Splitting the root node only requires setting the new branch as root.
	if current == a.root {
		current.setKey(current.key[len(commonPrefix):])
		newNode.setKey(newNode.key[len(commonPrefix):])

		newParent.addChild(current)
		newParent.addChild(newNode)

		a.root = newParent
		a.numNodes += 2
		a.numRecords++

		return
	}

	// Splitting the non-root node. Update the parent-child relationship
	// before manipulating the node keys of current and newNode.
	parent.removeChild(current)
	parent.addChild(newParent)

	// Reset current's nextSibling in prep for becoming a child of newParent.
	current.nextSibling = nil

	// Remove the common prefix from current and newNode.
	current.setKey(current.key[len(commonPrefix):])
	newNode.setKey(newNode.key[len(commonPrefix):])

	newParent.addChild(current)
	newParent.addChild(newNode)

	a.numNodes += 2
	a.numRecords++
}

// findNodeAndParent returns the node that matches the given key and its parent.
// The parent is nil if the discovered node is a root node.
func (a *Arc) findNodeAndParent(key []byte) (current *node, parent *node, err error) {
	if key == nil {
		return nil, nil, ErrNilKey
	}

	if a.empty() {
		return nil, nil, ErrKeyNotFound
	}

	current = a.root

	for {
		prefix := longestCommonPrefix(current.key, key)
		prefixLen := len(prefix)

		// Lack of a common prefix means that the key does not exist in the
		// tree, unless the current node is a root node.
		if prefix == nil && current != a.root {
			return nil, nil, ErrKeyNotFound
		}

		// Common prefix must be at least the length of the current key.
		// If not, the search key cannot exist in a Radix tree.
		if prefixLen != len(current.key) {
			return nil, nil, ErrKeyNotFound
		}

		// The prefix matches the current node's key.
		if prefixLen == len(key) {
			return current, parent, nil
		}

		if !current.hasChildren() {
			return nil, nil, ErrKeyNotFound
		}

		// Update the key for the next iteration, and then continue traversing.
		key = key[len(prefix):]
		parent = current
		current = current.findCompatibleChild(key)

		// The key does not exist if a compatible child is not found.
		if current == nil {
			return nil, nil, ErrKeyNotFound
		}
	}
}

// longestCommonPrefix compares the two given byte slices, and returns the
// longest common prefix. Memory-safety is ensured by establishing an index
// boundary based on the length of the shorter parameter.
func longestCommonPrefix(a, b []byte) []byte {
	minLen := len(a)

	if len(b) < minLen {
		minLen = len(b)
	}

	var i int

	for i = 0; i < minLen; i++ {
		if a[i] != b[i] {
			break
		}
	}

	if i == 0 {
		return nil
	}

	return a[:i]
}