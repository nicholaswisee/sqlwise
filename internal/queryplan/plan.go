package queryplan

import (
	"encoding/json"
	"errors"
)

// Node is one plan step. Actual values are nil unless the plan was analyzed.
type Node struct {
	Type        string
	Operation   string
	Relation    string
	Index       string
	Join        string
	StartupCost float64
	TotalCost   float64
	PlanRows    float64
	ActualTime  *float64
	ActualRows  *float64
	Loops       *float64
	Children    []Node
}

type Result struct {
	Dialect       string
	Mode          Mode
	SQLHash       string
	Warnings      []string
	Root          Node
	PlanningTime  *float64
	ExecutionTime *float64
}

type rawNode struct {
	Type        string    `json:"Node Type"`
	Operation   string    `json:"Operation"`
	Relation    string    `json:"Relation Name"`
	Index       string    `json:"Index Name"`
	Join        string    `json:"Join Type"`
	StartupCost float64   `json:"Startup Cost"`
	TotalCost   float64   `json:"Total Cost"`
	PlanRows    float64   `json:"Plan Rows"`
	ActualTime  *float64  `json:"Actual Total Time"`
	ActualRows  *float64  `json:"Actual Rows"`
	Loops       *float64  `json:"Actual Loops"`
	Plans       []rawNode `json:"Plans"`
}

func (r rawNode) node() Node {
	n := Node{Type: r.Type, Operation: r.Operation, Relation: r.Relation, Index: r.Index, Join: r.Join, StartupCost: r.StartupCost,
		TotalCost: r.TotalCost, PlanRows: r.PlanRows, ActualTime: r.ActualTime, ActualRows: r.ActualRows, Loops: r.Loops}
	for _, child := range r.Plans {
		n.Children = append(n.Children, child.node())
	}
	return n
}

// ParsePostgres reads EXPLAIN (FORMAT JSON) output.
func ParsePostgres(raw []byte) (Node, *float64, *float64, error) {
	var plans []struct {
		Plan          rawNode  `json:"Plan"`
		PlanningTime  *float64 `json:"Planning Time"`
		ExecutionTime *float64 `json:"Execution Time"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil {
		return Node{}, nil, nil, err
	}
	if len(plans) != 1 || plans[0].Plan.Type == "" {
		return Node{}, nil, nil, errors.New("unexpected plan format")
	}
	return plans[0].Plan.node(), plans[0].PlanningTime, plans[0].ExecutionTime, nil
}
