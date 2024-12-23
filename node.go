// Copyright Chrono Technologies LLC
// SPDX-License-Identifier: MIT

package arc

import "bytes"

// node represents an in-memory node of a Radix tree. This implementation is
// designed to be memory-efficient by maintaining a minimal set of fields for
// both node representation and persistence metadata. Consider memory overhead
// carefully before adding new fields to this struct.
type node struct {
	key         []byte // Path segment of the node.
	isRecord    bool   // True if the node contains a database record.
	blobValue   bool   // True if the value is stored in the blobStore.
	numChildren int    // Number of connected child nodes.
	firstChild  *node  // Pointer to the first child node.
	nextSibling *node  // Pointer to the adjacent sibling node.

	// Holds the node's content. For values less than or equal to 32 bytes,
	// it stores the content directly. For larger values, it stores a blobID
	// that references the content in the blobStore.
	data []byte
}

func newRecordNode(bs blobStore, key []byte, value []byte) *node {
	ret := &node{isRecord: true}
	ret.setKey(key)

	if value != nil {
		ret.setValue(bs, value)
	}

	return ret
}

// hasChidren returns true if the receiver node has children.
func (n node) hasChildren() bool {
	return n.firstChild != nil
}

// isLeaf returns true if the receiver node is a leaf node.
func (n node) isLeaf() bool {
	return n.firstChild == nil
}

// value returns a copy of the node's value.
func (n node) value(bs blobStore) []byte {
	if n.data == nil {
		return nil
	}

	if !n.blobValue {
		ret := make([]byte, len(n.data))
		copy(ret, n.data)

		return ret
	}

	// No need to copy the return value. blobStore handles it.
	return bs.get(n.data)
}

// forEachChild loops over the children of the node, and calls the given
// callback function on each visit.
func (n node) forEachChild(cb func(int, *node) error) error {
	if n.firstChild == nil {
		return nil
	}

	child := n.firstChild

	for i := 0; child != nil; i++ {
		if err := cb(i, child); err != nil {
			return err
		}

		child = child.nextSibling
	}

	return nil
}

// findChild returns the node's child that matches the given key.
func (n node) findChild(key []byte) (*node, error) {
	for child := n.firstChild; child != nil; child = child.nextSibling {
		if bytes.Equal(child.key, key) {
			return child, nil
		}
	}

	return nil, ErrKeyNotFound
}

// findCompatibleChild returns the first child that shares a common prefix.
func (n node) findCompatibleChild(key []byte) *node {
	for child := n.firstChild; child != nil; child = child.nextSibling {
		prefix := longestCommonPrefix(child.key, key)

		if len(prefix) > 0 {
			return child
		}
	}

	return nil
}

// setKey updates the node's key with the provided value.
func (n *node) setKey(key []byte) {
	n.key = key
}

// setValue sets the given value to the node and flags it as a record node.
func (n *node) setValue(bs blobStore, value []byte) {
	if n.blobValue {
		bs.release(n.data)
	}

	if len(value) <= inlineValueThreshold {
		n.data = value
		n.blobValue = false
	} else {
		id := bs.put(value)
		n.data = id.Slice()
		n.blobValue = true
	}

	n.isRecord = true
}

// deleteValue deletes the node's value and sets the data pointer to nil.
func (n *node) deleteValue(bs blobStore) {
	if n.blobValue {
		bs.release(n.data)
	}

	n.data = nil
}

// prependKey prepends the given prefix to the node's existing key.
func (n *node) prependKey(prefix []byte) {
	if len(prefix) == 0 {
		return
	}

	newKey := make([]byte, len(prefix)+len(n.key))

	copy(newKey, prefix)
	copy(newKey[len(prefix):], n.key)

	n.key = newKey
}

// addChild inserts the given child into the node's sorted linked-list of
// children. Children are maintained in ascending order by their key values.
func (n *node) addChild(child *node) {
	n.numChildren++

	// Empty list means the given child becomes the firstChild.
	if n.firstChild == nil {
		// Becoming a first child means there are no siblings.
		child.nextSibling = nil
		n.firstChild = child
		return
	}

	// Insert at start if the given child's key is smallest.
	if bytes.Compare(child.key, n.firstChild.key) < 0 {
		child.nextSibling = n.firstChild
		n.firstChild = child
		return
	}

	// Find the insertion point by advancing until we find a node whose next
	// sibling has a key greater than or equal to the given child's key, or
	// until we reach the end of the list.
	current := n.firstChild

	for current.nextSibling != nil && bytes.Compare(current.nextSibling.key, child.key) < 0 {
		current = current.nextSibling
	}

	// Insert the given child between current and its nextSibling.
	// current -> child -> current.nextSibling
	child.nextSibling = current.nextSibling
	current.nextSibling = child
}

// removeChild removes the child node that matches the given child's key.
func (n *node) removeChild(child *node) error {
	if n.firstChild == nil {
		return ErrKeyNotFound
	}

	// Special case: removing first child.
	if bytes.Equal(n.firstChild.key, child.key) {
		n.firstChild = n.firstChild.nextSibling
		n.numChildren--

		return nil
	}

	// Search for a node whose nextSibling matches the given child's key.
	current := n.firstChild

	for current.nextSibling != nil {
		next := current.nextSibling

		if bytes.Equal(next.key, child.key) {
			// Remove the node by updating the link to skip it.
			current.nextSibling = next.nextSibling
			n.numChildren--

			return nil
		}

		current = next
	}

	return ErrKeyNotFound
}

// shallowCopyFrom copies the properties from the src node to the receiver node.
// This function performs a shallow copy, meaning that the copied fields share
// memory references with the original and are not actual copies. The function
// is intended for cases where sustaining the receiver's address is necessary.
func (n *node) shallowCopyFrom(src *node) {
	n.key = src.key
	n.data = src.data
	n.isRecord = src.isRecord
	n.numChildren = src.numChildren
	n.firstChild = src.firstChild
	n.nextSibling = src.nextSibling
}
