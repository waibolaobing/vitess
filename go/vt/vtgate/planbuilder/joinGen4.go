/*
Copyright 2019 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package planbuilder

import (
	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
	"vitess.io/vitess/go/vt/sqlparser"
	"vitess.io/vitess/go/vt/vterrors"
	"vitess.io/vitess/go/vt/vtgate/engine"
	"vitess.io/vitess/go/vt/vtgate/semantics"
)

var _ logicalPlan = (*joinGen4)(nil)

// joinGen4 is used to build a Join primitive.
// It's used to build an inner join and only used by the Gen4 planner
type joinGen4 struct {
	// Left and Right are the nodes for the join.
	Left, Right logicalPlan
	Opcode      engine.JoinOpcode
	Cols        []int
	Vars        map[string]int
	Predicate   sqlparser.Expr

	gen4Plan
}

// WireupGen4 implements the logicalPlan interface
func (j *joinGen4) WireupGen4(semTable *semantics.SemTable) error {
	err := j.Left.WireupGen4(semTable)
	if err != nil {
		return err
	}
	return j.Right.WireupGen4(semTable)
}

// Primitive implements the logicalPlan interface
func (j *joinGen4) Primitive() engine.Primitive {
	return &engine.Join{
		Left:    j.Left.Primitive(),
		Right:   j.Right.Primitive(),
		Cols:    j.Cols,
		Vars:    j.Vars,
		Opcode:  j.Opcode,
		ASTPred: j.Predicate,
	}
}

// Inputs implements the logicalPlan interface
func (j *joinGen4) Inputs() []logicalPlan {
	return []logicalPlan{j.Left, j.Right}
}

// Rewrite implements the logicalPlan interface
func (j *joinGen4) Rewrite(inputs ...logicalPlan) error {
	if len(inputs) != 2 {
		return vterrors.New(vtrpcpb.Code_INTERNAL, "wrong number of children")
	}
	j.Left = inputs[0]
	j.Right = inputs[1]
	return nil
}

// ContainsTables implements the logicalPlan interface
func (j *joinGen4) ContainsTables() semantics.TableSet {
	return j.Left.ContainsTables().Merge(j.Right.ContainsTables())
}
