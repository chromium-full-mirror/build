// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package parse

import (
	"fmt"
	"io"
	"strings"
)

// NodeDump is a JSON-serializable representation of a ParseNode.
type NodeDump struct {
	Type         string     `json:"type"`
	Value        string     `json:"value,omitempty"`
	Children     []NodeDump `json:"child,omitempty"`
	BeginToken   string     `json:"begin_token,omitempty"`
	End          *NodeDump  `json:"end,omitempty"`
	AccessorKind string     `json:"accessor_kind,omitempty"`
}

// RenderDump renders a NodeDump as text, matching GN's output when running
// `gn format --dump_tree=text`.
func RenderDump(w io.Writer, dump NodeDump) error {
	return renderDumpAsText(w, dump, 0)
}

func renderDumpAsText(w io.Writer, dump NodeDump, indentLevel int) error {
	if dump.Type == "" {
		return fmt.Errorf("node does not have a type")
	}
	if dump.Type == "ACCESSOR" {
		// GN dumps accessors differently, instead of
		// NODE_TYPE(value)
		// it outputs
		// ACCESSOR
		//  value
		_, err := fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", indentLevel), dump.Type)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s%s", strings.Repeat(" ", indentLevel+1), dump.Value)
		if err != nil {
			return err
		}
	} else {
		// Everything else is dumped as NODE_TYPE(value)
		_, err := fmt.Fprintf(w, "%s%s", strings.Repeat(" ", indentLevel), dump.Type)
		if err != nil {
			return err
		}
		if dump.Value != "" {
			_, err = fmt.Fprintf(w, "(%s)", dump.Value)
			if err != nil {
				return err
			}
		}
	}
	_, err := io.WriteString(w, "\n")
	if err != nil {
		return err
	}
	for _, childNode := range dump.Children {
		err := renderDumpAsText(w, childNode, indentLevel+1)
		if err != nil {
			return fmt.Errorf("failed to dump sub-node: %w", err)
		}
	}
	return nil
}

// Dump returns a JSON-serializable of this node.
func (n *AccessorNode) Dump() NodeDump {
	dump := NodeDump{
		Type:  "ACCESSOR",
		Value: n.Base.Value(),
	}
	if n.Subscript != nil {
		dump.Children = []NodeDump{n.Subscript.Dump()}
		dump.AccessorKind = "subscript"
	} else if n.Member != nil {
		dump.Children = []NodeDump{n.Member.Dump()}
		dump.AccessorKind = "member"
	}
	return dump
}

// Dump returns a JSON-serializable of this node.
func (n *BlockNode) Dump() NodeDump {
	end := n.End.Dump()
	dump := NodeDump{
		Type:       "BLOCK",
		BeginToken: n.BeginToken.Value(),
		End:        &end,
	}
	for _, c := range n.Statements {
		dump.Children = append(dump.Children, c.Dump())
	}
	return dump
}

// Dump returns a JSON-serializable of this node.
func (n *FunctionCallNode) Dump() NodeDump {
	dump := NodeDump{
		Type:  "FUNCTION",
		Value: n.Function.Value(),
		Children: []NodeDump{
			n.Args.Dump(),
		},
	}
	if n.Block != nil {
		dump.Children = append(dump.Children, n.Block.Dump())
	}
	return dump
}

// Dump returns a JSON-serializable of this node.
func (n *IdentifierNode) Dump() NodeDump {
	return NodeDump{
		Type:  "IDENTIFIER",
		Value: n.Value.Value(),
	}
}

// Dump returns a JSON-serializable of this node.
func (n *ListNode) Dump() NodeDump {
	end := n.End.Dump()
	dump := NodeDump{
		Type:       "LIST",
		BeginToken: n.BeginToken.Value(),
		End:        &end,
	}
	for _, c := range n.Contents {
		dump.Children = append(dump.Children, c.Dump())
	}
	return dump
}

// Dump returns a JSON-serializable of this node.
func (n *LiteralNode) Dump() NodeDump {
	return NodeDump{
		Type:  "LITERAL",
		Value: n.Token.Value(),
	}
}

// Dump returns a JSON-serializable of this node.
func (n *BinaryOpNode) Dump() NodeDump {
	return NodeDump{
		Type:  "BINARY_OP",
		Value: n.Op.Value(),
		Children: []NodeDump{
			n.Left.Dump(),
			n.Right.Dump(),
		},
	}
}

// Dump returns a JSON-serializable of this node.
func (n *UnaryOpNode) Dump() NodeDump {
	return NodeDump{
		Type:  "UNARY_OP",
		Value: n.Op.Value(),
		Children: []NodeDump{
			n.Operand.Dump(),
		},
	}
}

// Dump returns a JSON-serializable of this node.
func (n *BlockCommentNode) Dump() NodeDump {
	return NodeDump{
		Type:  "BLOCK_COMMENT",
		Value: n.Comment.Value(),
	}
}

// Dump returns a JSON-serializable of this node.
func (n *EndNode) Dump() NodeDump {
	return NodeDump{
		Type:  "END",
		Value: n.Value.Value(),
	}
}
